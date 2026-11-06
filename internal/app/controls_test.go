package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPausePreservesSettingsAcrossRestart(t *testing.T) {
	home := t.TempDir()
	folder := filepath.Join(t.TempDir(), "Shared")
	if err := os.MkdirAll(folder, 0700); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(home)
	if err != nil {
		t.Fatal(err)
	}
	original := config{Mode: "session", Folder: folder, Enabled: true, Secret: "pairing-secret", Session: strings.Repeat("a", 32)}
	a.cfg = original
	if err := a.setPaused(true); err != nil {
		t.Fatal(err)
	}
	if !a.cfg.Paused || a.cfg.Secret != original.Secret || a.cfg.Folder != folder {
		t.Fatalf("pause lost configuration: %#v", a.cfg)
	}
	second, err := newApp(home)
	if err != nil {
		t.Fatal(err)
	}
	if !second.cfg.Paused || second.cfg.Secret != original.Secret {
		t.Fatalf("pause not persisted: %#v", second.cfg)
	}
	if err := second.startPeer(); err != nil {
		t.Fatal(err)
	}
	if state := second.engine.Snapshot().State; state != "Paused" {
		t.Errorf("expected Paused after restart; got %q", state)
	}
}

func TestPauseRequiresConfiguredFolder(t *testing.T) {
	a, err := newApp(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := a.setPaused(true); err == nil {
		t.Fatal("unconfigured pause unexpectedly succeeded")
	}
}

func TestPauseAPIAndDashboard(t *testing.T) {
	a, err := newApp(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "http://"+uiAddress+"/api/pause", strings.NewReader(`{"paused":true}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	a.uiHandler().ServeHTTP(response, request)
	if response.Code != http.StatusConflict {
		t.Errorf("expected 409 without configuration, got %d", response.Code)
	}
	for _, required := range []string{"id=\"currentversion\"", "id=\"pause\"", "id=\"autostart\"", "/api/pause", "/api/autostart"} {
		if !strings.Contains(pageHTML, required) {
			t.Errorf("dashboard missing %q", required)
		}
	}
	statusRequest := httptest.NewRequest(http.MethodGet, "http://"+uiAddress+"/api/status", nil)
	statusResponse := httptest.NewRecorder()
	a.uiHandler().ServeHTTP(statusResponse, statusRequest)
	var data struct {
		Paused    bool `json:"paused"`
		Autostart bool `json:"autostart"`
	}
	if err := json.Unmarshal(statusResponse.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	if data.Paused {
		t.Fatal("unconfigured app reports paused")
	}
}

func TestNotificationsOnlyOnNewError(t *testing.T) {
	a, err := newApp(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	a.setNotifier(func(title, msg string, warning bool) { n++ })
	a.setError("network unavailable")
	a.setError("network unavailable")
	a.setError("another error")
	if n != 2 {
		t.Fatalf("expected 2 unique notifications, got %d", n)
	}
}

func TestPauseResumeSessionLifecycle(t *testing.T) {
	a, err := newApp(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a.listenAddress = "127.0.0.1:0"
	folder := t.TempDir()
	c, err := a.engine.PrepareSession(folder, "")
	if err != nil {
		t.Fatal(err)
	}
	a.cfg = config{Mode: "session", Folder: folder, Session: c.ID, Secret: c.Secret, Enabled: true, Paused: true}
	if err := a.startPeer(); err != nil {
		t.Fatal(err)
	}
	if a.engine.Snapshot().State != "Paused" {
		t.Fatal("paused session should not listen")
	}
	if err := a.setPaused(false); err != nil {
		t.Fatal(err)
	}
	defer a.stopPeer()
	if a.cfg.Paused || len(a.engine.Snapshot().Links) != 1 {
		t.Fatal("resume should start equal-peer listener")
	}
	if err := a.setPaused(true); err != nil {
		t.Fatal(err)
	}
	if !a.cfg.Paused || a.engine.Snapshot().State != "Paused" {
		t.Fatal("pause should stop session")
	}
}
