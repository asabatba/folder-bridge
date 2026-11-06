package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"folderbridge/internal/update"
)

func TestV32ConfigurationPreservedWithUpdateDefaults(t *testing.T) {
	home := t.TempDir()
	original := `{"mode":"join","folder":"C:\\Shared","invite":"original-invite","secret":"original-secret","enabled":true,"paused":true}`
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	history := []byte("original sync history")
	os.WriteFile(filepath.Join(home, "baseline-test.json"), history, 0600)
	a, err := newApp(home)
	if err != nil {
		t.Fatal(err)
	}
	if a.cfg.Invite != "original-invite" || a.cfg.Secret != "original-secret" || !a.cfg.Enabled || !a.cfg.Paused {
		t.Fatal("v3.2 configuration changed")
	}
	if *a.cfg.Updates != update.DefaultSettings() {
		t.Fatalf("wrong update defaults: %+v", a.cfg.Updates)
	}
	s := update.DefaultSettings()
	s.Enabled = false
	s.SkippedVersion = "3.4.0"
	if err = a.updateSettings(s); err != nil {
		t.Fatal(err)
	}
	a, err = newApp(home)
	if err != nil {
		t.Fatal(err)
	}
	if a.cfg.Invite != "original-invite" || a.cfg.Secret != "original-secret" || !a.cfg.Paused || *a.cfg.Updates != s {
		t.Fatal("saving update settings changed sync configuration")
	}
	got, _ := os.ReadFile(filepath.Join(home, "baseline-test.json"))
	if !bytes.Equal(got, history) {
		t.Fatal("history modified")
	}
	// Old dashboard clients do not send updates in sync configuration requests.
	if err = a.configure(Config{Mode: "off"}); err != nil {
		t.Fatal(err)
	}
	if *a.cfg.Updates != s {
		t.Fatal("sync settings erased update settings")
	}
}
func TestUpdateRefusesUnpersistedSyncState(t *testing.T) {
	a, err := newApp(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a.syncPersistErr = errors.New("baseline disk write failed")
	if err = a.installUpdate(t.TempDir()); err == nil || !strings.Contains(err.Error(), "baseline disk write failed") {
		t.Fatalf("handoff ignored baseline persistence failure: %v", err)
	}
	if a.draining {
		t.Fatal("failed preparation left sync draining")
	}
}

func TestUpdateRoutesUseExistingDashboardProtection(t *testing.T) {
	a, err := newApp(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a.startUpdates()
	defer a.stopUpdates()
	handler := a.uiHandler()
	for _, tc := range []struct {
		path, host, origin, content string
		want                        int
	}{
		{"/api/updates/install", uiAddress, "http://evil.example", "application/json", 403},
		{"/api/updates/settings", "evil.example", "", "application/json", 403},
		{"/api/updates/install", uiAddress, "", "text/plain", 415},
		{"/api/updates/install", uiAddress, "", "application/json", 409},
	} {
		r := httptest.NewRequest("POST", "http://"+tc.host+tc.path, strings.NewReader(`{}`))
		r.Host = tc.host
		r.Header.Set("Origin", tc.origin)
		r.Header.Set("Content-Type", tc.content)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("%+v: HTTP %d %s", tc, w.Code, w.Body.String())
		}
	}
	for _, body := range []string{`{"autoInstall":true}`, `{"channel":"beta"}`, `{"checkIntervalHours":0}`} {
		r := httptest.NewRequest("POST", "http://"+uiAddress+"/api/updates/settings", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatal("invalid settings accepted", body, w.Code)
		}
	}
	r := httptest.NewRequest("GET", "http://"+uiAddress+"/api/updates", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	var status update.Status
	if err = json.Unmarshal(w.Body.Bytes(), &status); err != nil || status.CurrentVersion != update.Version {
		t.Fatal("current version missing", err)
	}
}

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
			a, err := newApp(home)
			if err != nil {
				t.Fatal(err)
			}
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
			peer := newPeer(Invite{Addr: u.Host, Token: secret, Fingerprint: fingerprint})
			defer peer.Close()
			defer unblock()
			cfg := Config{Mode: "join", Folder: clientDir, Invite: formatInvite(u.Host, secret, fingerprint), Enabled: true, Updates: a.cfg.Updates}
			a.cfg = cfg
			payload := strings.Repeat("safe streamed file contents\n", 20000)
			writeTestFile(t, clientDir, "active.txt", payload)
			var canceled atomic.Bool
			a.cancel = func() { canceled.Store(true) }
			syncDone := make(chan error, 1)
			if hosting {
				go func() {
					_, _, err := syncFolders(context.Background(), clientDir, peer, map[string]string{}, NewScanner(), false, func(string) {})
					syncDone <- err
				}()
			} else {
				go func() {
					a.runSync(context.Background(), cfg, peer, NewPersistentScanner(clientDir, home))
					syncDone <- nil
				}()
			}
			select {
			case <-started:
			case <-time.After(10 * time.Second):
				t.Fatal("real transfer did not enter write path")
			}
			installDone := make(chan error, 1)
			stage := t.TempDir()
			go func() { installDone <- a.installUpdate(stage) }()
			deadline := time.Now().Add(5 * time.Second)
			for {
				a.mu.RLock()
				draining := a.draining
				a.mu.RUnlock()
				if draining {
					break
				}
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
			if canceled.Load() {
				t.Fatal("active transfer context canceled")
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
				if err == nil {
					t.Fatal("invalid stage unexpectedly installed")
				}
			case <-time.After(10 * time.Second):
				t.Fatal("handoff did not resume after drain")
			}
			if got := readTestFile(t, hostDir, "active.txt"); got != payload {
				t.Fatal("active write interrupted")
			}
			if !hosting {
				base, err := loadBaseline(baselineFilename(home, clientDir, cfg.Invite))
				if err != nil || base["active.txt"] == "" {
					t.Fatal("baseline not persisted before handoff", err)
				}
			}
			if canceled.Load() {
				t.Fatal("failed preparation canceled synchronization")
			}
			a.mu.RLock()
			draining := a.draining
			a.mu.RUnlock()
			if draining {
				t.Fatal("failed preparation left synchronization blocked")
			}
		})
	}
}
