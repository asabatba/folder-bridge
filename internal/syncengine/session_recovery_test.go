package syncengine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSessionPreservesLocalVersionWhenRemoteConflictCopyUnavailable(t *testing.T) {
	a := testSessionNode(t, "")
	b := testSessionNode(t, testSessionInvite(a))
	low, high := a, b
	if low.self.ID > high.self.ID {
		low, high = high, low
	}
	sessionWrite(t, low, "note.txt", "base")
	sessionConverge(t, low, high)
	sessionWrite(t, low, "note.txt", "primary edit")
	sessionWrite(t, high, "note.txt", "only local copy")
	sessionObserve(t, low)
	sessionObserve(t, high)
	sessionExchange(t, low, high)
	w := sessionObserve(t, low) // publishes the deterministic conflict copy as a path
	live := liveVersions(low.state.Files["note.txt"])
	if len(live) != 2 {
		t.Fatal(live)
	}
	conflict := sessionConflictPath("note.txt", live[1])
	// Metadata is not proof that payload is available. Simulate the advertised
	// backup disappearing after the snapshot while high still has its own edit.
	if err := os.Remove(filepath.Join(low.cfg.Folder, conflict)); err != nil {
		t.Fatal(err)
	}
	delete(w.Contents, conflict)
	p := high.peer(low.self.Addresses[0], low.self.ID)
	defer p.Close()
	high.mu.Lock()
	_, err := high.apply(context.Background(), w, p, false, func(string) {})
	high.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if got := sessionRead(t, high, conflict); got != "only local copy" {
		t.Fatal("local concurrent version lost", got)
	}
	if got := sessionRead(t, high, "note.txt"); got != "primary edit" {
		t.Fatal(got)
	}
}

func TestSessionDashboardSnapshotDoesNotWaitForTransferLock(t *testing.T) {
	s := testSessionNode(t, "")
	e := New(filepath.Dir(s.filename), nil)
	e.session = s
	e.cfg = s.cfg
	done := make(chan Status, 1)
	s.mu.Lock()
	go func() { done <- e.Snapshot() }()
	select {
	case st := <-done:
		if len(st.Members) != 1 {
			t.Error("missing detached member view", st)
		}
	case <-time.After(250 * time.Millisecond):
		t.Error("dashboard blocked behind file transfer")
	}
	s.mu.Unlock()
}
