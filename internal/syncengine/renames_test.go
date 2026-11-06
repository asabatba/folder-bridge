package syncengine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"folderbridge/internal/fileops"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type syncFixture struct {
	hostDir, clientDir string
	host               *host
	server             *httptest.Server
	peer               *peer
	scanner            *scanner
	base               map[string]string
}

func newSyncFixture(t *testing.T) *syncFixture {
	t.Helper()
	f := &syncFixture{hostDir: t.TempDir(), clientDir: t.TempDir(), scanner: newScanner(), base: map[string]string{}}
	secret := strings.Repeat("a", 48)
	f.host = newHost(f.hostDir, secret)
	f.server = httptest.NewTLSServer(f.host.handler())
	u, err := url.Parse(f.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	pin := sha256.Sum256(f.server.TLS.Certificates[0].Certificate[0])
	f.peer = newPeer(invitation{Addr: u.Host, Token: secret, Fingerprint: hex.EncodeToString(pin[:])})
	t.Cleanup(func() { f.peer.Close(); f.server.Close() })
	return f
}
func (f *syncFixture) sync(t *testing.T) SyncResult {
	t.Helper()
	next, result, err := syncFolders(context.Background(), f.clientDir, f.peer, f.base, f.scanner, false, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	f.base = next
	return result
}
func assertMissing(t *testing.T, root, name string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(name))); !os.IsNotExist(err) {
		t.Fatalf("expected missing %s: %v", name, err)
	}
}
func TestRenameClientToHostWithoutTransfer(t *testing.T) {
	f := newSyncFixture(t)
	writeTestFile(t, f.clientDir, "folder/old.bin", strings.Repeat("a", 2<<20))
	f.sync(t)
	if err := os.Rename(filepath.Join(f.clientDir, "folder", "old.bin"), filepath.Join(f.clientDir, "folder", "new.bin")); err != nil {
		t.Fatal(err)
	}
	result := f.sync(t)
	if result.Renames != 1 || result.Uploaded != 0 || result.Downloaded != 0 || result.Deleted != 0 {
		t.Fatalf("unexpected result %+v", result)
	}
	if got := readTestFile(t, f.hostDir, "folder/new.bin"); len(got) != 2<<20 {
		t.Fatal("wrong file size")
	}
	assertMissing(t, f.hostDir, "folder/old.bin")
	result = f.sync(t)
	if result.Renames != 0 || result.Uploaded != 0 || result.Downloaded != 0 {
		t.Fatalf("rename repeated %+v", result)
	}
}
func TestRenameHostToClientWithoutTransfer(t *testing.T) {
	f := newSyncFixture(t)
	writeTestFile(t, f.hostDir, "old.txt", "stable content")
	f.sync(t)
	if err := os.MkdirAll(filepath.Join(f.hostDir, "nested"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(f.hostDir, "old.txt"), filepath.Join(f.hostDir, "nested", "new.txt")); err != nil {
		t.Fatal(err)
	}
	result := f.sync(t)
	if result.Renames != 1 || result.Downloaded != 0 || result.Uploaded != 0 || result.Deleted != 0 {
		t.Fatalf("unexpected result %+v", result)
	}
	if got := readTestFile(t, f.clientDir, "nested/new.txt"); got != "stable content" {
		t.Fatal(got)
	}
	assertMissing(t, f.clientDir, "old.txt")
}
func TestRenameAmbiguousDuplicateContentUsesTransfers(t *testing.T) {
	f := newSyncFixture(t)
	writeTestFile(t, f.hostDir, "a.txt", "same")
	writeTestFile(t, f.hostDir, "b.txt", "same")
	f.sync(t)
	if err := os.Rename(filepath.Join(f.clientDir, "a.txt"), filepath.Join(f.clientDir, "c.txt")); err != nil {
		t.Fatal(err)
	}
	result := f.sync(t)
	if result.Renames != 0 {
		t.Fatalf("duplicate hash should not optimize %+v", result)
	}
	if got := readTestFile(t, f.hostDir, "c.txt"); got != "same" {
		t.Fatal(got)
	}
	assertMissing(t, f.hostDir, "a.txt")
}
func TestRenamedAndModifiedUsesTransfer(t *testing.T) {
	f := newSyncFixture(t)
	writeTestFile(t, f.clientDir, "old.txt", "old bytes")
	f.sync(t)
	if err := os.Rename(filepath.Join(f.clientDir, "old.txt"), filepath.Join(f.clientDir, "new.txt")); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, f.clientDir, "new.txt", "new bytes")
	result := f.sync(t)
	if result.Renames != 0 {
		t.Fatalf("modified rename must be uploaded %+v", result)
	}
	if got := readTestFile(t, f.hostDir, "new.txt"); got != "new bytes" {
		t.Fatal(got)
	}
}
func TestDivergentMovesDoNotGuess(t *testing.T) {
	f := newSyncFixture(t)
	writeTestFile(t, f.hostDir, "old.txt", "same")
	f.sync(t)
	os.Rename(filepath.Join(f.hostDir, "old.txt"), filepath.Join(f.hostDir, "remote.txt"))
	os.Rename(filepath.Join(f.clientDir, "old.txt"), filepath.Join(f.clientDir, "local.txt"))
	result := f.sync(t)
	if result.Renames != 0 {
		t.Fatalf("divergent moves must not optimize %+v", result)
	}
	if got := readTestFile(t, f.hostDir, "local.txt"); got != "same" {
		t.Fatal(got)
	}
	if got := readTestFile(t, f.clientDir, "remote.txt"); got != "same" {
		t.Fatal(got)
	}
}
func TestMassRenameDoesNotTripDeletionGuard(t *testing.T) {
	f := newSyncFixture(t)
	for i := 0; i < 25; i++ {
		writeTestFile(t, f.hostDir, fmt.Sprintf("old-%02d.txt", i), fmt.Sprintf("distinct-%02d", i))
	}
	f.sync(t)
	for i := 0; i < 25; i++ {
		if err := os.Rename(filepath.Join(f.clientDir, fmt.Sprintf("old-%02d.txt", i)), filepath.Join(f.clientDir, fmt.Sprintf("new-%02d.txt", i))); err != nil {
			t.Fatal(err)
		}
	}
	result := f.sync(t)
	if result.Renames != 25 || result.Deleted != 0 {
		t.Fatalf("expected 25 moves, got %+v", result)
	}
}
func TestMoveRejectsOverwriteAndChangedSource(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "src.txt", "source")
	writeTestFile(t, root, "dst.txt", "existing")
	sum := sha256.Sum256([]byte("source"))
	hash := hex.EncodeToString(sum[:])
	if err := fileops.MoveUnchanged(root, "src.txt", "dst.txt", hash); err == nil {
		t.Fatal("destination overwrite allowed")
	}
	if got := readTestFile(t, root, "dst.txt"); got != "existing" {
		t.Fatal(got)
	}
	if err := fileops.MoveUnchanged(root, "src.txt", "new.txt", strings.Repeat("0", 64)); err == nil {
		t.Fatal("changed source allowed")
	}
	assertMissing(t, root, "new.txt")
	if err := fileops.MoveUnchanged(root, "src.txt", "../escape.txt", hash); err == nil {
		t.Fatal("path traversal allowed")
	}
}
func TestMoveEndpointRejectsIgnoredAndUnsafePaths(t *testing.T) {
	f := newSyncFixture(t)
	writeTestFile(t, f.hostDir, "old.txt", "content")
	writeTestFile(t, f.hostDir, ".folderbridgeignore", "ignored/\n")
	sum := sha256.Sum256([]byte("content"))
	hash := hex.EncodeToString(sum[:])
	if err := f.peer.move(context.Background(), "old.txt", "ignored/new.txt", hash); err == nil {
		t.Fatal("ignored target was accepted")
	}
	if err := f.peer.move(context.Background(), "old.txt", "../out.txt", hash); err == nil {
		t.Fatal("unsafe target was accepted")
	}
	if got := readTestFile(t, f.hostDir, "old.txt"); got != "content" {
		t.Fatal(got)
	}
}
func TestExplainIgnoreRules(t *testing.T) {
	rules := []ignoreFile{
		{Path: ".gitignore", Text: "# comment\n*.log\n!important.log\ncache/\n"},
		{Path: "sub/.folderbridgeignore", Text: "private*.txt\n"},
	}
	m, err := newIgnoreMatcher(rules)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		path    string
		ignored bool
		source  string
		line    int
		reason  string
	}{
		{"debug.log", true, ".gitignore", 2, ""},
		{"important.log", false, ".gitignore", 3, "Allowed"},
		{"cache/test.log", true, ".gitignore", 4, "Parent"},
		{"sub/private1.txt", true, "sub/.folderbridgeignore", 1, ""},
		{"sub/public.txt", false, "", 0, "No matching"},
		{".git/index", true, "FolderBridge internal policy", 0, "Internal"},
	}
	for _, tc := range cases {
		d := m.Explain(tc.path, false)
		if d.Ignored != tc.ignored || d.Source != tc.source || d.Line != tc.line || !strings.Contains(d.Reason, tc.reason) {
			t.Errorf("%s decision %+v", tc.path, d)
		}
		if d.Ignored != m.isIgnored(tc.path, false) {
			t.Errorf("explanation mismatches matcher: %s", tc.path)
		}
	}
}

func TestLegacyPeerWithoutMoveFeatureUsesOrdinarySync(t *testing.T) {
	hostDir, clientDir := t.TempDir(), t.TempDir()
	secret := strings.Repeat("b", 48)
	host := newHost(hostDir, secret)
	originalHandler := host.handler()
	shim := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/v2/manifest" {
			// Simulate v3.1: same manifest, but no atomic-rename advertisement.
			rec := httptest.NewRecorder()
			originalHandler.ServeHTTP(rec, r)
			if rec.Code != 200 {
				w.WriteHeader(rec.Code)
				w.Write(rec.Body.Bytes())
				return
			}
			var m manifestSnapshot
			if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
				t.Error(err)
				return
			}
			m.Features = nil
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(m)
			return
		}
		originalHandler.ServeHTTP(w, r)
	})
	srv := httptest.NewTLSServer(shim)
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	pin := sha256.Sum256(srv.TLS.Certificates[0].Certificate[0])
	peer := newPeer(invitation{Addr: u.Host, Token: secret, Fingerprint: hex.EncodeToString(pin[:])})
	defer peer.Close()
	scanner := newScanner()
	writeTestFile(t, clientDir, "old.txt", "legacy move data")
	base, result, err := syncFolders(context.Background(), clientDir, peer, map[string]string{}, scanner, false, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if result.Renames != 0 {
		t.Fatal(result)
	}
	if err := os.Rename(filepath.Join(clientDir, "old.txt"), filepath.Join(clientDir, "new.txt")); err != nil {
		t.Fatal(err)
	}
	_, result, err = syncFolders(context.Background(), clientDir, peer, base, scanner, false, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if result.Renames != 0 || result.Uploaded != 1 || result.Deleted != 1 {
		t.Fatalf("fallback sync failed %+v", result)
	}
	if got := readTestFile(t, hostDir, "new.txt"); got != "legacy move data" {
		t.Fatal(got)
	}
}

func TestIgnoreInspectorCombinesPeerRules(t *testing.T) {
	f := newSyncFixture(t)
	writeTestFile(t, f.hostDir, ".gitignore", "server-only.txt\n")
	writeTestFile(t, f.clientDir, ".folderbridgeignore", "local-only.txt\n")
	u, _ := url.Parse(f.server.URL)
	pin := sha256.Sum256(f.server.TLS.Certificates[0].Certificate[0])
	// The TLS test server uses a random port rather than FolderBridge's default.
	invite := "folderbridge://" + u.Host + "/" + f.peer.token + "?fp=" + hex.EncodeToString(pin[:])
	for _, path := range []string{"server-only.txt", "local-only.txt"} {
		got, err := ExplainIgnore(context.Background(), Options{Mode: "join", Folder: f.clientDir, Invite: invite}, path)
		if err != nil {
			t.Fatal(err)
		}
		if !got.Effective || got.Remote == nil || got.RemoteStatus != "Available" {
			t.Fatalf("%s: %+v", path, got)
		}
		if path == "server-only.txt" && !got.Remote.Ignored {
			t.Fatal("missing peer rule", got)
		}
		if path == "local-only.txt" && !got.Local.Ignored {
			t.Fatal("missing local rule", got)
		}
	}
}
