package syncengine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type heldUpdateBody struct {
	io.ReadCloser
	once    sync.Once
	started chan struct{}
	release <-chan struct{}
}

func (b *heldUpdateBody) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.started); <-b.release })
	return b.ReadCloser.Read(p)
}

func TestApprovedUpdateDrainsRealSyncBeforeHandoff(t *testing.T) {
	for _, hosting := range []bool{false, true} {
		name := "client cycle saves baseline"
		if hosting {
			name = "host write finishes"
		}
		t.Run(name, func(t *testing.T) {
			home, hostDir, clientDir := t.TempDir(), t.TempDir(), t.TempDir()
			a := New(home, nil)
			secret := "updater-drain-test-token"
			started, release := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			hostHandler := newHost(hostDir, secret).handler()
			if hosting {
				hostHandler = a.hostDuringUpdate(hostHandler)
			}
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "PUT" {
					r.Body = &heldUpdateBody{ReadCloser: r.Body, started: started, release: release}
				}
				hostHandler.ServeHTTP(w, r)
			}))
			defer srv.Close()
			u, _ := url.Parse(srv.URL)
			pin := sha256.Sum256(srv.TLS.Certificates[0].Certificate[0])
			fingerprint := hex.EncodeToString(pin[:])
			p := newPeer(invitation{Addr: u.Host, Token: secret, Fingerprint: fingerprint})
			defer p.Close()
			defer unblock()
			cfg := Options{Mode: "join", Folder: clientDir, Invite: formatInvite(u.Host, secret, fingerprint), Enabled: true}
			payload := strings.Repeat("safe streamed file contents\n", 20000)
			writeTestFile(t, clientDir, "active.txt", payload)
			var canceled atomic.Bool
			a.cancel = func() { canceled.Store(true) }
			syncDone := make(chan error, 1)
			if hosting {
				go func() {
					_, _, err := syncFolders(context.Background(), clientDir, p, map[string]string{}, newScanner(), false, func(string) {})
					syncDone <- err
				}()
			} else {
				go func() {
					a.runSync(context.Background(), cfg, p, newPersistentScanner(clientDir, home))
					syncDone <- nil
				}()
			}
			select {
			case <-started:
			case <-time.After(10 * time.Second):
				t.Fatal("real transfer did not enter write path")
			}
			installDone := make(chan error, 1)
			var handedOff atomic.Bool
			go func() {
				installDone <- a.Quiesce(func() error {
					handedOff.Store(true)
					if !hosting {
						base, err := loadBaseline(baselineFilename(home, clientDir, cfg.Invite))
						if err != nil || base["active.txt"] == "" {
							return errors.New("baseline not persisted before handoff")
						}
					}
					return errors.New("invalid stage")
				})
			}()
			deadline := time.Now().Add(5 * time.Second)
			for !a.Snapshot().Draining {
				if time.Now().After(deadline) {
					t.Fatal("drain never began")
				}
				time.Sleep(time.Millisecond)
			}
			select {
			case err := <-installDone:
				t.Fatalf("installation did not wait for active transfer: %v", err)
			case <-time.After(100 * time.Millisecond):
			}
			if canceled.Load() || handedOff.Load() {
				t.Fatal("active transfer canceled or premature handoff")
			}
			unblock()
			select {
			case err := <-syncDone:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("transfer did not drain")
			}
			select {
			case err := <-installDone:
				if err == nil || err.Error() != "invalid stage" {
					t.Fatal("unexpected handoff outcome", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("handoff did not resume after drain")
			}
			if got := readTestFile(t, hostDir, "active.txt"); got != payload {
				t.Fatal("active write interrupted")
			}
			if !handedOff.Load() || canceled.Load() || a.Snapshot().Draining {
				t.Fatal("failed handoff did not preserve and resume synchronization")
			}
		})
	}
}
func TestQuiesceRefusesUnpersistedState(t *testing.T) {
	e := New(t.TempDir(), nil)
	e.syncPersistErr = errors.New("baseline disk write failed")
	called := false
	err := e.Quiesce(func() error { called = true; return nil })
	if err == nil || !strings.Contains(err.Error(), "baseline disk write failed") || called || e.Snapshot().Draining {
		t.Fatalf("unsafe handoff: %v %v %+v", err, called, e.Snapshot())
	}
}
func TestSuccessfulQuiesceKeepsAdmissionClosed(t *testing.T) {
	e := New(t.TempDir(), nil)
	if err := e.Quiesce(func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if !e.Snapshot().Draining {
		t.Fatal("successful handoff resumed work")
	}
	called := false
	h := e.hostDuringUpdate(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("PUT", "http://peer/v1/file", strings.NewReader("new write")))
	if w.Code != 503 || called {
		t.Fatal("new write admitted during committed handoff", w.Code)
	}
	if err := e.Start(Options{}); err == nil {
		t.Fatal("new session started during committed handoff")
	}
	e.Stop()
}
