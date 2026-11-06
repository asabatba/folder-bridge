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

func TestSessionCompressedBatchesAndRenameWithoutPayload(t *testing.T) {
	a := testSessionNode(t, "")
	b := testSessionNode(t, testSessionInvite(a))
	for i := 0; i < 48; i++ {
		sessionWrite(t, a, fmt.Sprintf("small/%03d.txt", i), strings.Repeat(fmt.Sprintf("file %d ", i), 20))
	}
	r := sessionExchange(t, b, a)
	if r.Downloaded != 48 || r.BatchDownloads == 0 {
		t.Fatal("session lost compressed batching", r)
	}
	sessionConverge(t, a, b)
	// Unique hashes make all these moves unambiguous; even a bulk rename must
	// avoid the deletion guard and all file payload downloads.
	for i := 0; i < 48; i++ {
		from := fmt.Sprintf("small/%03d.txt", i)
		to := fmt.Sprintf("small/moved-%03d.txt", i)
		if err := os.Rename(filepath.Join(a.cfg.Folder, from), filepath.Join(a.cfg.Folder, to)); err != nil {
			t.Fatal(err)
		}
	}
	a.scanner.Invalidate("", true)
	r = sessionExchange(t, b, a)
	if r.Renames != 48 || r.Downloaded != 0 || r.Deleted != 0 {
		t.Fatal("session rename retransferred contents", r)
	}
	if got := sessionRead(t, b, "small/moved-047.txt"); !strings.Contains(got, "file 47") {
		t.Fatal(got)
	}
}

func TestSessionChangedSourceKeepsDestinationAndRetries(t *testing.T) {
	a := testSessionNode(t, "")
	b := testSessionNode(t, testSessionInvite(a))
	sessionWrite(t, a, "file", "before")
	sessionConverge(t, a, b)
	sessionWrite(t, a, "file", "advertised")
	w := sessionObserve(t, a)
	sessionWrite(t, a, "file", "changed during transfer")
	p := b.peer(a.self.Addresses[0], a.self.ID)
	defer p.Close()
	b.mu.Lock()
	_, err := b.apply(context.Background(), w, p, false, func(string) {})
	b.mu.Unlock()
	if err == nil {
		t.Fatal("changed source was accepted")
	}
	if got := sessionRead(t, b, "file"); got != "before" {
		t.Fatal("destination changed on rejected transfer", got)
	}
	sessionConverge(t, a, b)
	if got := sessionRead(t, b, "file"); got != "changed during transfer" {
		t.Fatal("retry did not reconcile new version", got)
	}
}

func TestSessionAuthenticationAndNoLegacyRoutes(t *testing.T) {
	a := testSessionNode(t, "")
	b := testSessionNode(t, testSessionInvite(a))
	p := b.peer(a.self.Addresses[0], a.self.ID)
	defer p.Close()
	for _, endpoint := range []string{"/v1/manifest", "/v2/manifest", "/v3/changes?since=0"} {
		_, err := b.request(context.Background(), p, "GET", endpoint, nil)
		var status *httpStatusError
		if !errors.As(err, &status) || status.Status != http.StatusNotFound {
			t.Fatal("legacy endpoint exposed", endpoint, err)
		}
	}
	p.token = "incorrect"
	_, err := b.request(context.Background(), p, "GET", "/v4/changes?since=0", nil)
	var status *httpStatusError
	if !errors.As(err, &status) || status.Status != 401 {
		t.Fatal("wrong session password accepted", err)
	}
	p.token = b.cfg.Secret
	p.headers["X-FolderBridge-Device"] = strings.Repeat("0", 64)
	_, err = b.request(context.Background(), p, "GET", "/v4/changes?since=0", nil)
	if !errors.As(err, &status) || status.Status != 403 {
		t.Fatal("certificate/device mismatch accepted", err)
	}
	p.headers["X-FolderBridge-Device"] = b.self.ID
	p.headers["X-FolderBridge-Session"] = strings.Repeat("0", 32)
	_, err = b.request(context.Background(), p, "GET", "/v4/changes?since=0", nil)
	if !errors.As(err, &status) || status.Status != 401 {
		t.Fatal("cross-session request accepted", err)
	}
}

func TestSessionNewMemberAddressWinsOverStaleGossip(t *testing.T) {
	a := testSessionNode(t, "")
	b := testSessionNode(t, testSessionInvite(a))
	c := testSessionNode(t, testSessionInvite(a))
	sessionConverge(t, a, b, c)
	stale := c.state.Members[b.self.ID]
	fresh := b.self
	fresh.Revision++
	fresh.Addresses = []string{"127.0.0.1:12345"}
	if err := a.learnMembers(fresh, nil); err != nil {
		t.Fatal(err)
	}
	if err := a.learnMembers(c.self, map[string]sessionMember{stale.ID: stale}); err != nil {
		t.Fatal(err)
	}
	got := a.state.Members[b.self.ID]
	if got.Revision != fresh.Revision || got.Addresses[0] != fresh.Addresses[0] {
		t.Fatal("stale gossip restored old endpoint", got)
	}
}

func TestSessionLongPollIsStableWhenIdle(t *testing.T) {
	a := testSessionNode(t, "")
	b := testSessionNode(t, testSessionInvite(a))
	sessionConverge(t, a, b)
	a.changes.changeMu.Lock()
	before := a.changes.changeSeq
	a.changes.changeMu.Unlock()
	sessionExchange(t, b, a)
	a.changes.changeMu.Lock()
	after := a.changes.changeSeq
	a.changes.changeMu.Unlock()
	if before != after {
		t.Fatal("idle exchange generated another notification", before, after)
	}
	p := b.peer(a.self.Addresses[0], a.self.ID)
	defer p.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := b.request(ctx, p, "GET", fmt.Sprintf("/v4/changes?since=%d", after), nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("idle long poll did not wait", err)
	}
}

func TestSessionAutomaticThreeEngineLifecycle(t *testing.T) {
	var engines []*Engine
	var roots []string
	invite := ""
	for i := 0; i < 3; i++ {
		home, root := t.TempDir(), t.TempDir()
		e := New(home, nil)
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
		links := e.Snapshot().Links
		if len(links) != 1 {
			t.Fatal("missing member invite", links)
		}
		invite = links[0]
	}
	wait := func(check func() bool) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for !check() {
			if time.Now().After(deadline) {
				for _, e := range engines {
					t.Log(e.Snapshot())
				}
				t.Fatal("automatic session did not converge")
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	wait(func() bool { return len(engines[0].Snapshot().Members) == 3 && len(engines[2].Snapshot().Members) == 3 })
	if err := os.WriteFile(filepath.Join(roots[0], "automatic.txt"), []byte("all members"), 0600); err != nil {
		t.Fatal(err)
	}
	wait(func() bool {
		// Both recipients must have materialized the original before the next
		// edit. Editing an as-yet-unreceived path is legitimately concurrent.
		for _, root := range roots[1:] {
			b, _ := os.ReadFile(filepath.Join(root, "automatic.txt"))
			if string(b) != "all members" {
				return false
			}
		}
		return true
	})
	engines[0].Stop()
	if err := os.WriteFile(filepath.Join(roots[1], "automatic.txt"), []byte("without creator"), 0600); err != nil {
		t.Fatal(err)
	}
	wait(func() bool {
		b, _ := os.ReadFile(filepath.Join(roots[2], "automatic.txt"))
		return string(b) == "without creator"
	})
}
