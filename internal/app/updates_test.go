package app

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"folderbridge/internal/update"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestV32ConfigurationPreservedWithUpdateDefaults(t *testing.T) {
	home := t.TempDir()
	original := `{"mode":"join","folder":"C:\\Shared","invite":"original-invite","secret":"original-secret","enabled":true,"paused":true}`
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	history := []byte("original sync history")
	if err := os.WriteFile(filepath.Join(home, "baseline-test.json"), history, 0600); err != nil {
		t.Fatal(err)
	}
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
	got, err := os.ReadFile(filepath.Join(home, "baseline-test.json"))
	if err != nil || !bytes.Equal(got, history) {
		t.Fatal("history modified", err)
	}
	if err = a.configure(config{Mode: "off"}); err != nil {
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
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	defer srv.Close()
	pin := sha256.Sum256(srv.TLS.Certificates[0].Certificate[0])
	invite := "folderbridge://" + strings.TrimPrefix(srv.URL, "https://") + "/" + strings.Repeat("a", 48) + "?fp=" + hex.EncodeToString(pin[:])
	a.cfg = config{Mode: "join", Folder: t.TempDir(), Invite: invite, Enabled: true, Updates: a.cfg.Updates}
	// Seed the existing on-disk baseline format, without reaching into engine
	// state or exporting persistence internals just to make this adapter test pass.
	key := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(a.cfg.Folder)) + "\n" + invite))
	baseline := filepath.Join(a.home, "baseline-"+hex.EncodeToString(key[:12])+".json")
	if err := os.WriteFile(baseline, []byte("{broken json"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.startPeer(); err != nil {
		t.Fatal(err)
	}
	defer a.stopPeer()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(a.engine.Snapshot().Error, "Baseline:") {
		if time.Now().After(deadline) {
			t.Fatal("baseline error did not reach status")
		}
		time.Sleep(time.Millisecond)
	}
	if err = a.installUpdate(t.TempDir()); err == nil || !strings.Contains(err.Error(), "persist synchronization state") {
		t.Fatalf("handoff ignored baseline persistence failure: %v", err)
	}
	if a.draining || a.engine.Snapshot().Draining {
		t.Fatal("failed preparation left sync draining")
	}
}

func TestFailedPreparationResumesSynchronization(t *testing.T) {
	a, err := newApp(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := a.installUpdate(t.TempDir()); err == nil {
		t.Fatal("invalid staging directory installed")
	}
	if a.draining || a.engine.Snapshot().Draining {
		t.Fatal("failed preparation left synchronization blocked")
	}
	if err := a.configure(config{Mode: "off"}); err != nil {
		t.Fatal("configuration remained blocked after failure", err)
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
