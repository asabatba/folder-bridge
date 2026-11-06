package syncengine_test

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"folderbridge/internal/syncengine"
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

func TestUnreadableBaselineReportsErrorWithoutRewriting(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	var manifests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/manifest" {
			manifests.Add(1)
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	pin := sha256.Sum256(server.TLS.Certificates[0].Certificate[0])
	invite := "folderbridge://" + strings.TrimPrefix(server.URL, "https://") + "/" + strings.Repeat("a", 48) + "?fp=" + hex.EncodeToString(pin[:])
	key := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(root)) + "\n" + invite))
	baseline := filepath.Join(home, "baseline-"+hex.EncodeToString(key[:12])+".json")
	const broken = "{broken json"
	if err := os.WriteFile(baseline, []byte(broken), 0600); err != nil {
		t.Fatal(err)
	}
	e := syncengine.New(home, nil)
	defer e.Stop()
	if err := e.Start(syncengine.Options{Mode: "join", Folder: root, Invite: invite, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(e.Snapshot().Error, "Baseline:") {
		if time.Now().After(deadline) {
			t.Fatal("baseline error did not reach status", e.Snapshot())
		}
		time.Sleep(time.Millisecond)
	}
	e.Stop()
	if manifests.Load() != 0 {
		t.Fatal("reconciliation continued with an unreadable baseline")
	}
	if got, err := os.ReadFile(baseline); err != nil || string(got) != broken {
		t.Fatal("unreadable baseline was overwritten", err)
	}
}
