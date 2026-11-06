package app

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newSessionTestApp(t *testing.T) *application {
	t.Helper()
	a, err := newApp(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a.listenAddress = "127.0.0.1:0"
	t.Cleanup(a.stopPeer)
	return a
}

func TestSessionCreateJoinStopResumeAndLeave(t *testing.T) {
	a, b := newSessionTestApp(t), newSessionTestApp(t)
	folderA, folderB := t.TempDir(), t.TempDir()
	writeTestFile(t, folderA, "keep.txt", "existing file")
	if err := a.configure(config{Mode: "create", Folder: folderA, Name: "Office PC"}); err != nil {
		t.Fatal(err)
	}
	if a.cfg.Mode != "session" || a.cfg.Session == "" || a.cfg.Name != "Office PC" {
		t.Fatal(a.cfg)
	}
	links := a.engine.Snapshot().Links
	if len(links) != 1 {
		t.Fatal(links)
	}
	if err := b.configure(config{Mode: "join", Folder: folderB, Invite: links[0], Name: "Laptop"}); err != nil {
		t.Fatal(err)
	}
	if b.cfg.Mode != "session" || b.cfg.Session != a.cfg.Session || b.cfg.Secret != a.cfg.Secret || b.cfg.Invite != "" {
		t.Fatal("persistent host/guest roles remain", a.cfg, b.cfg)
	}
	original := a.cfg
	if err := a.configure(config{Mode: "off"}); err != nil {
		t.Fatal(err)
	}
	if a.cfg.Enabled || a.cfg.Session != original.Session || a.cfg.Folder != folderA {
		t.Fatal("stop lost session", a.cfg)
	}
	if err := a.configure(config{Mode: "session", Folder: folderA}); err != nil {
		t.Fatal(err)
	}
	if len(a.engine.Snapshot().Links) != 1 {
		t.Fatal("resume lost invitation")
	}
	if err := a.configure(config{Mode: "create", Folder: t.TempDir()}); err == nil {
		t.Fatal("active session replaced without explicit leave")
	}
	if err := a.configure(config{Mode: "leave"}); err != nil {
		t.Fatal(err)
	}
	if a.cfg.Session != "" || a.cfg.Enabled {
		t.Fatal("leave retained active session", a.cfg)
	}
	if got := readTestFile(t, folderA, "keep.txt"); got != "existing file" {
		t.Fatal("leave removed user files", got)
	}
	// Rejoin the SAME session without discarding its durable history.
	if err := a.configure(config{Mode: "join", Folder: folderA, Invite: b.engine.Snapshot().Links[0]}); err != nil {
		t.Fatal(err)
	}
	if a.cfg.Session != original.Session {
		t.Fatal("rejoin changed session")
	}
}

func TestSessionLegacyUpgradeRequiresExplicitFreshPairing(t *testing.T) {
	home := t.TempDir()
	folder := t.TempDir()
	old := config{Mode: "host", Folder: folder, Secret: "old pairing secret", Enabled: true}
	raw, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(home, "config.json"), raw, 0600)
	baseline := filepath.Join(home, "baseline-old.json")
	os.WriteFile(baseline, []byte("old history"), 0600)
	a, err := newApp(home)
	if err != nil {
		t.Fatal(err)
	}
	a.listenAddress = "127.0.0.1:0"
	defer a.stopPeer()
	if err := a.startPeer(); err == nil {
		t.Fatal("legacy configuration silently started")
	}
	unchanged, _ := os.ReadFile(filepath.Join(home, "config.json"))
	if string(unchanged) != string(raw) {
		t.Fatal("startup rewrote legacy settings")
	}
	writeTestFile(t, folder, ".folderbridgeignore", "secret.txt\n")
	if err := a.configure(config{Mode: "create", Folder: folder}); err == nil {
		t.Fatal("old ignore policy lost silently")
	}
	if err := os.Rename(filepath.Join(folder, ".folderbridgeignore"), filepath.Join(folder, ".fbignore")); err != nil {
		t.Fatal(err)
	}
	if err := a.configure(config{Mode: "create", Folder: folder}); err != nil {
		t.Fatal(err)
	}
	backup, err := os.ReadFile(filepath.Join(home, "legacy-config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved config
	if err := json.Unmarshal(backup, &saved); err != nil || saved != old {
		t.Fatal("old configuration not backed up", saved, err)
	}
	history, _ := os.ReadFile(baseline)
	if string(history) != "old history" {
		t.Fatal("old baseline modified")
	}
}

func TestSessionControlsRejectLegacyAndForgedCredentials(t *testing.T) {
	a := newSessionTestApp(t)
	if err := a.configure(config{Mode: "host", Folder: t.TempDir(), Enabled: true}); err == nil {
		t.Fatal("old host role accepted")
	}
	if err := a.configure(config{Mode: "session", Session: strings.Repeat("a", 32), Secret: strings.Repeat("b", 48), Folder: t.TempDir()}); err == nil {
		t.Fatal("unprepared credentials accepted")
	}
	if err := a.configure(config{Mode: "join", Folder: t.TempDir(), Invite: "folderbridge://192.168.1.2:48713/" + strings.Repeat("a", 48) + "?fp=" + strings.Repeat("b", 64)}); err == nil {
		t.Fatal("old invitation accepted")
	}
}

func TestSessionDashboardAndStatus(t *testing.T) {
	a := newSessionTestApp(t)
	if err := a.configure(config{Mode: "create", Folder: t.TempDir(), Name: "Named computer"}); err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"Create session", "Join session", "id=\"members\"", ".fbignore", "id=\"refreshmember\""} {
		if !strings.Contains(pageHTML, required) {
			t.Fatal("dashboard missing", required)
		}
	}
	for _, forbidden := range []string{"value=\"host\"", "Host folder", "Join another PC", "two-computer folder"} {
		if strings.Contains(pageHTML, forbidden) {
			t.Fatal("old dashboard role remains", forbidden)
		}
	}
	rec := httptest.NewRecorder()
	a.uiHandler().ServeHTTP(rec, httptest.NewRequest("GET", "http://"+uiAddress+"/api/status", nil))
	var status struct {
		Session, Mode, Name string
		Legacy              bool
		Members             []struct {
			Name, State string
			Local       bool
		}
		Invite string
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.Session != a.cfg.Session || status.Mode != "session" || status.Name != "Named computer" || status.Legacy || len(status.Members) != 1 || !status.Members[0].Local || status.Invite != "" {
		t.Fatal(status)
	}
}

func TestSessionRefreshAddressRequiresSameSession(t *testing.T) {
	a, b := newSessionTestApp(t), newSessionTestApp(t)
	if err := a.configure(config{Mode: "create", Folder: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if err := b.configure(config{Mode: "create", Folder: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if err := a.configure(config{Mode: "refresh", Invite: b.engine.Snapshot().Links[0]}); err == nil {
		t.Fatal("cross-session member accepted")
	}
}
