package syncengine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func sessionHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}
func testVersion(device string, n uint64, clock versionClock, text string) fileVersion {
	return fileVersion{Device: device, Sequence: n, Clock: clock, File: fileInfo{Hash: sessionHash(text), Size: int64(len(text)), MTime: 1}}
}

func TestSessionCausalMergeLaws(t *testing.T) {
	a, b, c := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)
	av := testVersion(a, 1, versionClock{a: 1}, "a")
	bv := testVersion(b, 1, versionClock{b: 1}, "b")
	cv := testVersion(c, 1, versionClock{a: 1, c: 1}, "c")
	merge := func(a, b []fileVersion) []fileVersion {
		t.Helper()
		v, err := mergeVersions(a, b)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	left := merge(merge([]fileVersion{av}, []fileVersion{bv}), []fileVersion{cv})
	right := merge([]fileVersion{av}, merge([]fileVersion{bv}, []fileVersion{cv}))
	if !reflect.DeepEqual(left, right) || !reflect.DeepEqual(left, merge([]fileVersion{cv}, merge([]fileVersion{bv}, []fileVersion{av}))) || !reflect.DeepEqual(left, merge(left, left)) {
		t.Fatal("merge does not converge", left, right)
	}
	if len(left) != 2 || left[0].Device != b || left[1].Device != c {
		t.Fatal("causally superseded version retained", left)
	}
	// Reusing an event identifier with different bytes is a protocol/state error.
	changed := av
	changed.File.Hash = sessionHash("different")
	if _, err := mergeVersions([]fileVersion{av}, []fileVersion{changed}); err == nil {
		t.Fatal("accepted conflicting event identity")
	}
}

func TestSessionEqualBytesRetainCausality(t *testing.T) {
	a, b := strings.Repeat("a", 64), strings.Repeat("b", 64)
	av := testVersion(a, 1, versionClock{a: 1}, "same")
	bv := testVersion(b, 1, versionClock{b: 1}, "same")
	merged, err := mergeVersions([]fileVersion{av}, []fileVersion{bv})
	if err != nil || len(merged) != 2 || len(liveVersions(merged)) != 1 {
		t.Fatal("equal bytes lost history or created duplicate physical copies", merged, err)
	}
	del := fileVersion{Device: a, Sequence: 2, Clock: versionClock{a: 2, b: 1}}
	merged, err = mergeVersions(merged, []fileVersion{del})
	if err != nil || len(merged) != 1 || len(liveVersions(merged)) != 0 {
		t.Fatal("observed delete did not supersede equal-content versions", merged, err)
	}
}

func testSessionNode(t *testing.T, invite string) *sessionRuntime {
	t.Helper()
	home, root := t.TempDir(), t.TempDir()
	e := New(home, nil)
	c, err := e.PrepareSession(root, invite)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Options{Mode: "session", Folder: root, Session: c.ID, Secret: c.Secret, Enabled: true, Name: filepath.Base(home)}
	s, err := newSessionRuntime(home, cfg, "127.0.0.1:0", func() {})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.cancel(); s.server.Close() })
	return s
}
func testSessionInvite(s *sessionRuntime) string {
	return formatSessionInvite(SessionCredentials{ID: s.cfg.Session, Secret: s.cfg.Secret}, s.self.Addresses[0], s.self.ID)
}
func sessionWrite(t *testing.T, s *sessionRuntime, name, text string) {
	t.Helper()
	p := filepath.Join(s.cfg.Folder, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	s.scanner.Invalidate(name, false)
}
func sessionRead(t *testing.T, s *sessionRuntime, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(s.cfg.Folder, filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
func sessionObserve(t *testing.T, s *sessionRuntime) sessionWire {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	w, err := s.snapshot(false)
	if err != nil {
		t.Fatal(err)
	}
	return w
}
func sessionExchange(t *testing.T, to, from *sessionRuntime) SyncResult {
	t.Helper()
	w := sessionObserve(t, to)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r, err := to.exchange(ctx, from.self, sessionHello{Self: to.self, Members: w.Members}, false, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func sessionConverge(t *testing.T, nodes ...*sessionRuntime) {
	t.Helper()
	for round := 0; round < 3; round++ {
		for _, to := range nodes {
			for _, from := range nodes {
				if to != from {
					sessionExchange(t, to, from)
				}
			}
		}
	}
}

func TestSessionThreeMembersCreatorOfflineAndRestart(t *testing.T) {
	a := testSessionNode(t, "")
	b := testSessionNode(t, testSessionInvite(a))
	c := testSessionNode(t, testSessionInvite(b))
	sessionWrite(t, a, "shared.txt", "first")
	sessionConverge(t, a, b, c)
	if sessionRead(t, c, "shared.txt") != "first" {
		t.Fatal("third member did not receive relayed file")
	}
	for _, s := range []*sessionRuntime{a, b, c} {
		if len(s.state.Members) != 3 {
			t.Fatal("membership did not propagate", s.self.ID, len(s.state.Members))
		}
	}
	a.server.Close()
	sessionWrite(t, b, "shared.txt", "creator offline")
	sessionConverge(t, b, c)
	if sessionRead(t, c, "shared.txt") != "creator offline" {
		t.Fatal("session still depends on creator")
	}
	// Resume the actual persisted causal state with the same device certificate.
	cfg, home := c.cfg, filepath.Dir(c.filename)
	c.server.Close()
	c.cancel()
	resumed, err := newSessionRuntime(home, cfg, "127.0.0.1:0", func() {})
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.server.Close()
	defer resumed.cancel()
	sessionWrite(t, b, "after-restart.txt", "retained membership")
	sessionConverge(t, b, resumed)
	if sessionRead(t, resumed, "after-restart.txt") != "retained membership" {
		t.Fatal("restart lost session")
	}
}

func TestSessionOfflineReturnDoesNotResurrectDeletedFiles(t *testing.T) {
	a := testSessionNode(t, "")
	b := testSessionNode(t, testSessionInvite(a))
	c := testSessionNode(t, testSessionInvite(a))
	sessionWrite(t, a, "deleted.txt", "old")
	sessionConverge(t, a, b, c)
	if err := os.Remove(filepath.Join(a.cfg.Folder, "deleted.txt")); err != nil {
		t.Fatal(err)
	}
	a.scanner.Invalidate("deleted.txt", false)
	sessionConverge(t, a, b) // C remains offline with the old file.
	sessionConverge(t, b, c)
	for _, s := range []*sessionRuntime{b, c} {
		if _, err := os.Stat(filepath.Join(s.cfg.Folder, "deleted.txt")); !os.IsNotExist(err) {
			t.Fatal("offline member resurrected deletion", err)
		}
	}
	if len(liveVersions(c.state.Files["deleted.txt"])) != 0 {
		t.Fatal("delete history was not retained")
	}
}

func TestSessionConcurrentEditsConvergeWithoutConflictStorm(t *testing.T) {
	a := testSessionNode(t, "")
	b := testSessionNode(t, testSessionInvite(a))
	c := testSessionNode(t, testSessionInvite(a))
	sessionWrite(t, a, "note.txt", "base")
	sessionConverge(t, a, b, c)
	sessionWrite(t, a, "note.txt", "edit A")
	sessionWrite(t, b, "note.txt", "edit B")
	sessionWrite(t, c, "note.txt", "edit C")
	sessionObserve(t, a)
	sessionObserve(t, b)
	sessionObserve(t, c)
	sessionConverge(t, a, b, c)
	primary := sessionRead(t, a, "note.txt")
	expected := map[string]bool{"edit A": true, "edit B": true, "edit C": true}
	for _, s := range []*sessionRuntime{a, b, c} {
		entries, err := os.ReadDir(s.cfg.Folder)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 3 || sessionRead(t, s, "note.txt") != primary {
			t.Fatal("nonconvergent conflict files", len(entries))
		}
		found := map[string]bool{}
		for _, entry := range entries {
			found[sessionRead(t, s, entry.Name())] = true
		}
		if !reflect.DeepEqual(found, expected) {
			t.Fatal("concurrent contents lost", found)
		}
	}
	sessionConverge(t, c, b, a)
	for _, s := range []*sessionRuntime{a, b, c} {
		entries, _ := os.ReadDir(s.cfg.Folder)
		if len(entries) != 3 {
			t.Fatal("repeated exchanges generated conflicts", len(entries))
		}
	}
}

func TestSessionConcurrentEditWinsOverDelete(t *testing.T) {
	a := testSessionNode(t, "")
	b := testSessionNode(t, testSessionInvite(a))
	sessionWrite(t, a, "note", "base")
	sessionConverge(t, a, b)
	os.Remove(filepath.Join(a.cfg.Folder, "note"))
	a.scanner.Invalidate("note", false)
	sessionWrite(t, b, "note", "edited offline")
	sessionObserve(t, a)
	sessionObserve(t, b)
	sessionConverge(t, a, b)
	if sessionRead(t, a, "note") != "edited offline" || sessionRead(t, b, "note") != "edited offline" {
		t.Fatal("deletion destroyed concurrent edit")
	}
}

func TestSessionIgnoresAreDeviceLocalAndUnignoreDoesNotDelete(t *testing.T) {
	a := testSessionNode(t, "")
	b := testSessionNode(t, testSessionInvite(a))
	c := testSessionNode(t, testSessionInvite(a))
	// Don't transfer the policy file itself, keeping intentionally different local
	// policies without introducing another special configuration mechanism.
	sessionWrite(t, b, ".fbignore", ".fbignore\nprivate.txt\n")
	sessionWrite(t, a, "private.txt", "shared by A and C")
	sessionConverge(t, a, b, c)
	if _, err := os.Stat(filepath.Join(b.cfg.Folder, "private.txt")); !os.IsNotExist(err) {
		t.Fatal("ignored file transferred", err)
	}
	if sessionRead(t, c, "private.txt") != "shared by A and C" {
		t.Fatal("B exclusion blocked A/C")
	}
	sessionWrite(t, b, ".fbignore", ".fbignore\n")
	sessionConverge(t, a, b, c)
	if sessionRead(t, b, "private.txt") != "shared by A and C" {
		t.Fatal("unignore treated absence as deletion")
	}
	sessionWrite(t, b, ".fbignore", ".fbignore\nprivate.txt\n")
	sessionObserve(t, b)
	os.Remove(filepath.Join(b.cfg.Folder, "private.txt"))
	b.scanner.Invalidate("private.txt", false)
	sessionConverge(t, a, b, c)
	if sessionRead(t, a, "private.txt") != "shared by A and C" {
		t.Fatal("ignored local absence propagated deletion")
	}
}

func TestSessionPendingWriteRecovery(t *testing.T) {
	a := testSessionNode(t, "")
	sessionWrite(t, a, "file", "before")
	sessionObserve(t, a)
	old := a.state.Applied["file"]
	old.Pending = &pendingFile{Hash: sessionHash("after"), Seen: copyClock(old.Seen)}
	a.state.Applied["file"] = old
	if err := saveSessionState(a.filename, a.state); err != nil {
		t.Fatal(err)
	}
	before := a.state.Counter
	sessionWrite(t, a, "file", "after") // crash between file installation and state save
	sessionObserve(t, a)
	if a.state.Counter != before || a.state.Applied["file"].Pending != nil || a.state.Applied["file"].Hash != sessionHash("after") {
		t.Fatal("own completed write mistaken for local edit")
	}
}

func TestSessionDamagedHistoryFailsClosed(t *testing.T) {
	a := testSessionNode(t, "")
	a.server.Close()
	a.cancel()
	if err := os.WriteFile(a.filename, []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := newSessionRuntime(filepath.Dir(a.filename), a.cfg, "127.0.0.1:0", func() {}); err == nil {
		t.Fatal("damaged session silently reset")
	}
	got, _ := os.ReadFile(a.filename)
	if string(got) != "{broken" {
		t.Fatal("damaged history overwritten")
	}
	os.Remove(a.filename)
	if _, err := newSessionRuntime(filepath.Dir(a.filename), a.cfg, "127.0.0.1:0", func() {}); err == nil {
		t.Fatal("missing history silently reset")
	}
}

func TestSessionBulkDeletionRequiresLocalApproval(t *testing.T) {
	a := testSessionNode(t, "")
	b := testSessionNode(t, testSessionInvite(a))
	for i := 0; i < 20; i++ {
		sessionWrite(t, a, fmt.Sprintf("file-%02d", i), "contents")
	}
	sessionConverge(t, a, b)
	for i := 0; i < 20; i++ {
		os.Remove(filepath.Join(a.cfg.Folder, fmt.Sprintf("file-%02d", i)))
	}
	a.scanner.Invalidate("", true)
	a.mu.Lock()
	_, err := a.snapshot(false)
	a.mu.Unlock()
	if err == nil || !strings.HasPrefix(err.Error(), "Safety stop:") {
		t.Fatal("local bulk deletion was not stopped", err)
	}
	a.mu.Lock()
	_, err = a.snapshot(true)
	a.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	w := sessionObserve(t, a)
	b.mu.Lock()
	_, err = b.apply(context.Background(), w, nil, false, func(string) {})
	b.mu.Unlock()
	if err == nil || !strings.HasPrefix(err.Error(), "Safety stop:") {
		t.Fatal("remote bulk deletion was not stopped", err)
	}
	if _, err := os.Stat(filepath.Join(b.cfg.Folder, "file-00")); err != nil {
		t.Fatal("guard removed file", err)
	}
	b.mu.Lock()
	_, err = b.apply(context.Background(), w, nil, true, func(string) {})
	b.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(b.cfg.Folder, "file-00")); !os.IsNotExist(err) {
		t.Fatal("approved deletion not applied", err)
	}
}

func TestSessionInvitationsAndLegacyIgnoreTransition(t *testing.T) {
	a := testSessionNode(t, "")
	if err := ValidateInvite(testSessionInvite(a)); err != nil {
		t.Fatal(err)
	}
	old := formatInvite("192.168.1.2", strings.Repeat("a", 48), strings.Repeat("b", 64))
	if err := ValidateInvite(old); err == nil {
		t.Fatal("old invitation accepted")
	}
	for _, host := range []string{"8.8.8.8:48713", "example.com:48713", "0.0.0.0:48713", "224.0.0.1:48713", "127.0.0.1:0"} {
		if validLANAddress(host) {
			t.Fatal("accepted non-LAN endpoint", host)
		}
	}
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, ".folderbridgeignore"), []byte("secret"), 0600)
	if _, err := New(t.TempDir(), nil).PrepareSession(root, ""); err == nil {
		t.Fatal("fresh pairing allowed obsolete ignore file silently")
	}
	if isIgnoreFilename(".folderbridgeignore") || !isIgnoreFilename(".fbignore") {
		t.Fatal("ignore alias policy wrong")
	}
}

func TestSessionWireRejectsUnsafeAndForgedHistory(t *testing.T) {
	a := testSessionNode(t, "")
	w := sessionObserve(t, a)
	wire, err := json.Marshal(w)
	if err != nil {
		t.Fatal(err)
	}
	if err = decodeSessionJSON(strings.NewReader(string(wire)+"{}"), &sessionWire{}); err == nil {
		t.Fatal("trailing message accepted")
	}
	w.Files["../escape"] = []fileVersion{testVersion(a.self.ID, 1, versionClock{a.self.ID: 1}, "bad")}
	if err := validateWire(w, a.cfg.Session, a.self.ID); err == nil {
		t.Fatal("unsafe history accepted")
	}
}
