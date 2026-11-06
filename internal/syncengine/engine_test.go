package syncengine_test

import (
	"crypto/sha256"
	"encoding/hex"
	"folderbridge/internal/syncengine"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPublicPausedLifecycleAndDetachedHistory(t *testing.T) {
	e := syncengine.New(t.TempDir(), nil)
	defer e.Stop()
	if err := e.Start(syncengine.Options{Mode: "host", Enabled: true, Paused: true}); err != nil {
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
func TestCandidateInitializationRequiresReadableLegacyBaseline(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	invite := "folderbridge://127.0.0.1:48713/" + strings.Repeat("a", 48) + "?fp=" + strings.Repeat("b", 64)
	// An independent fixture of the existing on-disk name, not a new storage format.
	key := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(root)) + "\n" + invite))
	baseline := filepath.Join(home, "baseline-"+hex.EncodeToString(key[:12])+".json")
	if err := os.WriteFile(baseline, []byte("{broken json"), 0600); err != nil {
		t.Fatal(err)
	}
	e := syncengine.New(home, nil)
	defer e.Stop()
	err := e.Start(syncengine.Options{Mode: "join", Folder: root, Invite: invite, Enabled: true, CheckBaseline: true})
	if err == nil || !strings.Contains(err.Error(), "cannot load synchronization baseline") {
		t.Fatal("candidate accepted unreadable baseline", err)
	}
}
