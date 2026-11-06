package syncengine_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"folderbridge/internal/syncengine"
)

func TestPublicPausedLifecycleAndDetachedHistory(t *testing.T) {
	e := syncengine.New(t.TempDir(), nil)
	defer e.Stop()
	if err := e.Start(syncengine.Options{Mode: "session", Enabled: true, Paused: true}); err != nil {
		t.Fatal(err)
	}
	if e.Snapshot().State != "Paused" {
		t.Fatal("paused session started work")
	}
	for i := 0; i < 25; i++ {
		e.RecordEvent("event")
	}
	snapshot := e.Snapshot()
	if len(snapshot.Events) != 20 {
		t.Fatal("history bound changed", len(snapshot.Events))
	}
	snapshot.Events[0] = "mutated by caller"
	if e.Snapshot().Events[0] == snapshot.Events[0] {
		t.Fatal("snapshot exposes live history")
	}
	e.Stop()
	if e.Snapshot().State != "Stopped" {
		t.Fatal("stop did not publish state")
	}
}

func TestLegacyBaselineNeverMigratedOrRewritten(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	baseline := filepath.Join(home, "baseline-legacy.json")
	const broken = "{broken json"
	if err := os.WriteFile(baseline, []byte(broken), 0600); err != nil {
		t.Fatal(err)
	}
	e := syncengine.New(home, nil)
	defer e.Stop()
	if err := e.Start(syncengine.Options{Mode: "join", Folder: root, Invite: "old-link", Enabled: true, ListenAddress: "127.0.0.1:0"}); err == nil {
		t.Fatal("old role silently started or migrated")
	}
	if got, err := os.ReadFile(baseline); err != nil || string(got) != broken {
		t.Fatal("legacy baseline overwritten", err)
	}
	c, err := e.PrepareSession(root, "")
	if err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(home, "session-"+c.ID+".json")
	if err := os.WriteFile(filename, []byte(broken), 0600); err != nil {
		t.Fatal(err)
	}
	if err := e.Start(syncengine.Options{Mode: "session", Session: c.ID, Secret: c.Secret, Folder: root, Enabled: true, ListenAddress: "127.0.0.1:0"}); err == nil || !strings.Contains(err.Error(), "history unavailable") {
		t.Fatal("unreadable history did not fail closed", err)
	}
	if got, _ := os.ReadFile(filename); string(got) != broken {
		t.Fatal("damaged session overwritten")
	}
}

func TestPublicSessionLifecycleUsesIsolatedListener(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	e := syncengine.New(home, nil)
	c, err := e.PrepareSession(root, "")
	if err != nil {
		t.Fatal(err)
	}
	cfg := syncengine.Options{Mode: "session", Session: c.ID, Secret: c.Secret, Folder: root, Enabled: true, ListenAddress: "127.0.0.1:0"}
	for i := 0; i < 3; i++ {
		if err := e.Start(cfg); err != nil {
			t.Fatal(err)
		}
		if err := e.Start(cfg); err == nil {
			t.Fatal("double-start leaked listener")
		}
		deadline := time.Now().Add(3 * time.Second)
		for !strings.Contains(e.Snapshot().State, "Session ready") {
			if time.Now().After(deadline) {
				t.Fatal(e.Snapshot())
			}
			time.Sleep(time.Millisecond)
		}
		st := e.Snapshot()
		if len(st.Links) != 1 || len(st.Members) != 1 || !st.Members[0].Local {
			t.Fatal(st)
		}
		st.Members[0].Name = "tampered"
		st.Links[0] = "tampered"
		if e.Snapshot().Members[0].Name == "tampered" || e.Snapshot().Links[0] == "tampered" {
			t.Fatal("live status exposed")
		}
		e.Stop()
		if e.Snapshot().State != "Stopped" {
			t.Fatal(e.Snapshot())
		}
	}
}
