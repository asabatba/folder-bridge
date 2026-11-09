package syncengine

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The restricted node has no listening socket at all. It can dial the other
// node, but a direct connection back cannot make this test pass.
func TestSessionOutboundOnlyBidirectional(t *testing.T) {
	var engines []*Engine
	var roots []string
	invite := ""
	for i := 0; i < 2; i++ {
		e := New(t.TempDir(), nil)
		root := t.TempDir()
		c, err := e.PrepareSession(root, invite)
		if err != nil {
			t.Fatal(err)
		}
		if err := e.Start(Options{Mode: "session", Session: c.ID, Secret: c.Secret, Folder: root, Enabled: true, ListenAddress: "127.0.0.1:0"}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(e.Stop)
		engines = append(engines, e)
		roots = append(roots, root)
		invite = e.Snapshot().Links[0]
	}
	restricted := engines[1].session
	if err := restricted.server.Close(); err != nil {
		t.Fatal(err)
	}
	wait := func(label string, check func() bool) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for !check() {
			if time.Now().After(deadline) {
				for _, e := range engines {
					st := e.Snapshot()
					t.Logf("state=%s error=%s members=%+v", st.State, st.Error, st.Members)
				}
				t.Fatal(label)
			}
			time.Sleep(25 * time.Millisecond)
		}
	}
	write := func(i int, name, contents string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(roots[i], name), []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
		engines[i].session.scanner.Invalidate(name, false)
		engines[i].RequestSync()
	}
	has := func(i int, name, contents string) bool {
		b, err := os.ReadFile(filepath.Join(roots[i], name))
		return err == nil && string(b) == contents
	}
	write(1, "outbound.txt", "from restricted")
	wait("reachable member cannot pull from outbound-only member", func() bool { return has(0, "outbound.txt", "from restricted") })
	// Drop all established reverse sockets, as a network interruption would.
	engines[0].session.closeReverse()
	write(1, "reconnected.txt", "after disconnect")
	wait("reverse sockets did not reconnect", func() bool { return has(0, "reconnected.txt", "after disconnect") })
	// Exceeds the small-file batch limit and exercises streaming /v4/file.
	large := strings.Repeat("large payload ", 100000)
	write(1, "large.bin", large)
	wait("large reverse transfer failed", func() bool { return has(0, "large.bin", large) })
	write(0, "inbound.txt", "to restricted")
	wait("restricted member cannot receive", func() bool { return has(1, "inbound.txt", "to restricted") })
	for i := 0; i < 24; i++ {
		write(1, fmt.Sprintf("batch-%02d", i), fmt.Sprintf("contents %d", i))
	}
	wait("batch did not cross reverse connection", func() bool {
		for i := 0; i < 24; i++ {
			if !has(0, fmt.Sprintf("batch-%02d", i), fmt.Sprintf("contents %d", i)) {
				return false
			}
		}
		return true
	})
	if err := os.Rename(filepath.Join(roots[1], "outbound.txt"), filepath.Join(roots[1], "renamed.txt")); err != nil {
		t.Fatal(err)
	}
	engines[1].RequestSync()
	wait("rename did not converge", func() bool {
		_, err := os.Stat(filepath.Join(roots[0], "outbound.txt"))
		return os.IsNotExist(err) && has(0, "renamed.txt", "from restricted")
	})
	if err := os.Remove(filepath.Join(roots[1], "renamed.txt")); err != nil {
		t.Fatal(err)
	}
	engines[1].RequestSync()
	wait("deletion did not converge", func() bool { _, err := os.Stat(filepath.Join(roots[0], "renamed.txt")); return os.IsNotExist(err) })
	wait("reverse-connected member still marked unavailable", func() bool {
		for _, m := range engines[0].Snapshot().Members {
			if !m.Local {
				return m.State == "Connected" && m.Error == ""
			}
		}
		return false
	})
	stopped := make(chan struct{})
	go func() { engines[1].Stop(); engines[0].Stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not join reverse handlers/workers")
	}
}

func TestSessionReverseAuthentication(t *testing.T) {
	a := testSessionNode(t, "")
	b := testSessionNode(t, testSessionInvite(a))
	// Wrong secret, session or certificate pin must never install a tunnel.
	for _, mutate := range []func(){
		func() { b.cfg.Secret = strings.Repeat("0", 48) },
		func() { b.cfg.Session = strings.Repeat("0", 32) },
	} {
		original := b.cfg
		mutate()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err := b.serveReverse(ctx, a.self.Addresses[0], a.self.ID)
		cancel()
		b.cfg = original
		if err == nil || a.reversePeer(b.self.ID) != nil {
			t.Fatal("unauthenticated tunnel accepted")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	err := b.serveReverse(ctx, a.self.Addresses[0], strings.Repeat("0", 64))
	cancel()
	if err == nil || a.reversePeer(b.self.ID) != nil {
		t.Fatal("wrong certificate pin accepted")
	}
	ctx, cancel = context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.serveReverse(ctx, a.self.Addresses[0], a.self.ID) }()
	t.Cleanup(func() { cancel(); a.closeReverse(); <-done })
	deadline := time.Now().Add(2 * time.Second)
	for a.reversePeer(b.self.ID) == nil {
		if time.Now().After(deadline) {
			t.Fatal("authenticated reverse connection not installed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	p := a.reversePeer(b.self.ID)
	// The original per-request checks remain mandatory inside the TLS tunnel.
	original := p.token
	p.token = "incorrect"
	_, err = a.request(context.Background(), p, "GET", "/v4/changes?since=0", nil)
	var status *httpStatusError
	if !errors.As(err, &status) || status.Status != http.StatusUnauthorized {
		t.Fatal("inner authentication bypassed", err)
	}
	p.token = original
	p.headers["X-FolderBridge-Device"] = strings.Repeat("0", 64)
	_, err = a.request(context.Background(), p, "GET", "/v4/changes?since=0", nil)
	if !errors.As(err, &status) || status.Status != http.StatusForbidden {
		t.Fatal("inner certificate binding bypassed", err)
	}
	p.headers["X-FolderBridge-Device"] = a.self.ID
	resp, err := a.request(context.Background(), p, "GET", "/v4/changes?since=0", nil)
	if err != nil {
		t.Fatal("valid reverse request failed", err)
	}
	var change changeResponse
	err = decodeSessionJSON(resp.Body, &change)
	resp.Body.Close()
	if err != nil {
		t.Fatal("invalid reverse response", err)
	}
	_, err = p.request(context.Background(), "POST", "/v4/reverse", nil, map[string]string{"Connection": "Upgrade", "Upgrade": reverseProtocol})
	if !errors.As(err, &status) || status.Status != http.StatusNotFound {
		t.Fatal("nested transport upgrade accepted", err)
	}
}
