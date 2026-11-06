package app

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStandaloneVersionAndNoUpdateRoutes(t *testing.T) {
	a, err := newApp(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	handler := a.uiHandler()
	status := httptest.NewRecorder()
	handler.ServeHTTP(status, httptest.NewRequest("GET", "http://"+uiAddress+"/api/status", nil))
	var data struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(status.Body.Bytes(), &data); err != nil || data.Version != Version {
		t.Fatal("build version missing from normal status", data, err)
	}
	for _, path := range []string{"/api/updates", "/api/updates/check", "/api/updates/download", "/api/updates/settings", "/api/updates/install", "/api/updates/skip", "/api/updates/retry"} {
		for _, method := range []string{"GET", "POST"} {
			r := httptest.NewRequest(method, "http://"+uiAddress+path, strings.NewReader(`{}`))
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != 404 {
				t.Errorf("removed route %s %s returned HTTP %d", method, path, w.Code)
			}
		}
	}
	page := httptest.NewRecorder()
	handler.ServeHTTP(page, httptest.NewRequest("GET", "http://"+uiAddress+"/", nil))
	if page.Code != 200 {
		t.Fatal("dashboard no longer served")
	}
	for _, forbidden := range []string{"/api/updates", "id=\"update", "Check for updates", "<h2>Updates</h2>"} {
		if strings.Contains(page.Body.String(), forbidden) {
			t.Errorf("dashboard retains updater surface %q", forbidden)
		}
	}
}

func TestConfigurationStoresOnlySyncSettings(t *testing.T) {
	home := t.TempDir()
	a, err := newApp(home)
	if err != nil {
		t.Fatal(err)
	}
	a.cfg = config{Mode: "join", Folder: t.TempDir(), Invite: "pairing-invite", Secret: "pairing-secret", Enabled: true, Paused: true}
	if err := a.saveConfig(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(home, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"mode", "folder", "invite", "secret", "enabled", "paused"} {
		if _, ok := fields[name]; !ok {
			t.Fatal("missing sync setting", name)
		}
	}
	if len(fields) != 6 {
		t.Fatal("configuration contains non-sync settings", fields)
	}
	reloaded, err := newApp(home)
	if err != nil || reloaded.cfg != a.cfg {
		t.Fatal("sync configuration failed round trip", err)
	}
}

func TestNoUpdaterSourceOrCommands(t *testing.T) {
	root := repositoryRoot(t)
	for _, removed := range []string{"internal/update", "cmd/folderbridge-updater", "cmd/folderbridge-release"} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(removed))); !os.IsNotExist(err) {
			t.Errorf("removed source still exists: %s (%v)", removed, err)
		}
	}
}
