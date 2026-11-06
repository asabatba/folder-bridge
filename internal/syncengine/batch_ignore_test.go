package syncengine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func testPeer(t *testing.T, handler http.Handler) *peer {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	u, _ := url.Parse(server.URL)
	pin := sha256.Sum256(server.TLS.Certificates[0].Certificate[0])
	peer := newPeer(invitation{Addr: u.Host, Token: "secret", Fingerprint: hex.EncodeToString(pin[:])})
	t.Cleanup(peer.Close)
	return peer
}

func TestIgnoreMatcherSemantics(t *testing.T) {
	matcher, err := newIgnoreMatcher([]ignoreFile{
		{Path: ".gitignore", Text: "#comment\n*.log\n!important.log\n/output/\n/root-only.txt\n**/generated/*.bin\ncache/\n!cache/keep.txt\n\\#literal\n"},
		{Path: "sub/.gitignore", Text: "!important.log\n/hidden.txt\n"},
	})
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]bool{
		"test.log": true, "one/two/test.log": true, "important.log": false,
		"sub/important.log": false, "sub/other.log": true,
		"output/x.txt": true, "sub/output/x.txt": false,
		"root-only.txt": true, "sub/root-only.txt": false,
		"generated/a.bin": true, "x/generated/a.bin": true, "x/y/generated/a.bin": true, "x/generated/a.txt": false,
		"cache/keep.txt": true, "cache/extra.txt": true,
		"#literal": true, "sub/hidden.txt": true, "other/hidden.txt": false,
	}
	for name, want := range cases {
		if got := matcher.isIgnored(name, false); got != want {
			t.Errorf("%s ignored=%v, want %v", name, got, want)
		}
	}
}

func TestScanHonorsNestedRulesAndPrunesDirectories(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, ".folderbridgeignore", ".env\n")
	writeTestFile(t, root, ".gitignore", "build/\n*.log\n")
	writeTestFile(t, root, "build/object.bin", "excluded")
	writeTestFile(t, root, ".git/config", "never include")
	writeTestFile(t, root, ".env", "secret")
	writeTestFile(t, root, "sub/.gitignore", "!keep.log\n")
	writeTestFile(t, root, "sub/keep.log", "include")
	writeTestFile(t, root, "sub/drop.log", "exclude")
	writeTestFile(t, root, "sub/code.go", "include")
	snapshot, err := newScanner().ScanManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, included := range []string{"sub/code.go", "sub/keep.log", ".gitignore", ".folderbridgeignore", "sub/.gitignore"} {
		if _, ok := snapshot.Files[included]; !ok {
			t.Errorf("missing %s", included)
		}
	}
	for _, excluded := range []string{".env", "build/object.bin", ".git/config", "sub/drop.log"} {
		if _, ok := snapshot.Files[excluded]; ok {
			t.Errorf("unexpectedly included %s", excluded)
		}
	}
}

func TestCompressedBatchesReplacePerFileRequests(t *testing.T) {
	hostDir, clientDir := t.TempDir(), t.TempDir()
	for i := 0; i < 48; i++ {
		writeTestFile(t, hostDir, fmt.Sprintf("from-host/%03d.txt", i), strings.Repeat("hello host ", 20))
		writeTestFile(t, clientDir, fmt.Sprintf("from-client/%03d.txt", i), strings.Repeat("hello client ", 20))
	}
	h := newHost(hostDir, "secret")
	var mu sync.Mutex
	requests := map[string]int{}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests[r.Method+" "+r.URL.Path]++
		mu.Unlock()
		h.handler().ServeHTTP(w, r)
	})
	peer := testPeer(t, handler)
	baseline, res, err := syncFolders(context.Background(), clientDir, peer, map[string]string{}, newScanner(), false, func(msg string) { t.Log(msg) })
	if err != nil {
		t.Fatal(err)
	}
	if len(baseline) != 96 || res.Uploaded != 48 || res.Downloaded != 48 {
		t.Fatalf("wrong sync: %+v, baseline size %d", res, len(baseline))
	}
	if res.BatchUploads != 1 || res.BatchDownloads != 1 {
		t.Errorf("didn't batch both directions: %+v", res)
	}
	mu.Lock()
	if requests["PUT /v1/file"] != 0 || requests["GET /v1/file"] != 0 {
		t.Errorf("individual transfers used: %v", requests)
	}
	if requests["POST /v2/batch/put"] != 1 || requests["POST /v2/batch/get"] != 1 {
		t.Errorf("expected one archive request per direction: %v", requests)
	}
	mu.Unlock()
	if got := readTestFile(t, clientDir, "from-host/047.txt"); !strings.Contains(got, "hello host") {
		t.Fatal(got)
	}
	if got := readTestFile(t, hostDir, "from-client/047.txt"); !strings.Contains(got, "hello client") {
		t.Fatal(got)
	}
	// Next cycle should do no transfers.
	_, res, err = syncFolders(context.Background(), clientDir, peer, baseline, newScanner(), false, func(string) {})
	if err != nil || res.Uploaded != 0 || res.Downloaded != 0 {
		t.Fatalf("unexpected re-transfer: %+v %v", res, err)
	}
}

func TestBatchTransfersKeepLargeFilesIndividual(t *testing.T) {
	hostDir, clientDir := t.TempDir(), t.TempDir()
	for i := 0; i < 10; i++ {
		writeTestFile(t, clientDir, fmt.Sprintf("small/%02d", i), "contents")
	}
	writeTestFile(t, clientDir, "large.bin", strings.Repeat("x", int(maxBatchFileBytes+1)))
	peer := testPeer(t, newHost(hostDir, "secret").handler())
	_, res, err := syncFolders(context.Background(), clientDir, peer, map[string]string{}, newScanner(), false, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if res.BatchUploads != 1 || res.Uploaded != 11 {
		t.Fatalf("incorrect batching: %+v", res)
	}
	if got := readTestFile(t, hostDir, "large.bin"); len(got) != int(maxBatchFileBytes+1) {
		t.Fatal("large transfer incomplete")
	}
}

func TestIgnoredPathsNeverDeleteSyncedFiles(t *testing.T) {
	hostDir, clientDir := t.TempDir(), t.TempDir()
	writeTestFile(t, hostDir, "data/private.txt", "keep both copies")
	peer := testPeer(t, newHost(hostDir, "secret").handler())
	scanner := newScanner()
	base, res, err := syncFolders(context.Background(), clientDir, peer, map[string]string{}, scanner, false, func(string) {})
	if err != nil || res.Downloaded != 1 {
		t.Fatal(res, err)
	}
	writeTestFile(t, clientDir, ".folderbridgeignore", "data/private.txt\n")
	base, _, err = syncFolders(context.Background(), clientDir, peer, base, scanner, false, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if _, tracked := base["data/private.txt"]; tracked {
		t.Fatal("ignored path remained in baseline")
	}
	if got := readTestFile(t, hostDir, "data/private.txt"); got != "keep both copies" {
		t.Fatal(got)
	}
	if got := readTestFile(t, clientDir, "data/private.txt"); got != "keep both copies" {
		t.Fatal(got)
	}
	// Both have copies after removing ignore. Neither should be deleted.
	if err := os.Remove(filepath.Join(clientDir, ".folderbridgeignore")); err != nil {
		t.Fatal(err)
	}
	base, _, err = syncFolders(context.Background(), clientDir, peer, base, scanner, false, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	// The other PC's synced ignore file is deleted on this pass; its rules
	// disappear from the next manifest, so the following pass re-merges files.
	base, _, err = syncFolders(context.Background(), clientDir, peer, base, scanner, false, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := base["data/private.txt"]; !ok {
		t.Fatal("file not merged back")
	}
	if got := readTestFile(t, hostDir, "data/private.txt"); got != "keep both copies" {
		t.Fatal(got)
	}
}

func TestIgnoreUnionAcrossBothComputers(t *testing.T) {
	hostDir, clientDir := t.TempDir(), t.TempDir()
	writeTestFile(t, hostDir, ".gitignore", "*.cache\n")
	writeTestFile(t, hostDir, "host.cache", "not for other PC")
	writeTestFile(t, clientDir, "client.cache", "also stays local")
	peer := testPeer(t, newHost(hostDir, "secret").handler())
	baseline, _, err := syncFolders(context.Background(), clientDir, peer, map[string]string{}, newScanner(), false, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(hostDir, "client.cache")); !os.IsNotExist(err) {
		t.Fatal("uploaded ignored file")
	}
	if _, err := os.Stat(filepath.Join(clientDir, "host.cache")); !os.IsNotExist(err) {
		t.Fatal("downloaded ignored file")
	}
	if _, ok := baseline["client.cache"]; ok {
		t.Fatal("ignored item in baseline")
	}
	if got := readTestFile(t, clientDir, ".gitignore"); got != "*.cache\n" {
		t.Fatal(got)
	}
}

func TestBatchUploadProtectsExistingModifiedFile(t *testing.T) {
	hostDir, clientDir := t.TempDir(), t.TempDir()
	for i := 0; i < 10; i++ {
		writeTestFile(t, clientDir, fmt.Sprintf("file%02d.txt", i), "incoming")
	}
	peer := testPeer(t, newHost(hostDir, "secret").handler())
	manifest, err := newScanner().ScanManifest(clientDir)
	if err != nil {
		t.Fatal(err)
	}
	var batch []batchFile
	for i := 0; i < 10; i++ {
		name := fmt.Sprintf("file%02d.txt", i)
		batch = append(batch, batchFile{Path: name, Info: manifest.Files[name], ExpectedDestination: ""})
	}
	writeTestFile(t, hostDir, "file05.txt", "different existing file")
	if err := peer.putBatch(context.Background(), clientDir, batch); err == nil {
		t.Fatal("expected guarded batch rejection")
	}
	if got := readTestFile(t, hostDir, "file05.txt"); got != "different existing file" {
		t.Fatal("existing file was clobbered")
	}
}

func TestBatchLargeCollectionSplitsIntoSeveralArchives(t *testing.T) {
	hostDir, clientDir := t.TempDir(), t.TempDir()
	for i := 0; i < 300; i++ {
		writeTestFile(t, clientDir, fmt.Sprintf("trees/%03d.txt", i), "small content")
	}
	peer := testPeer(t, newHost(hostDir, "secret").handler())
	base, res, err := syncFolders(context.Background(), clientDir, peer, map[string]string{}, newScanner(), false, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if res.BatchUploads != 3 || res.Uploaded != 300 || len(base) != 300 {
		t.Fatalf("unexpected chunking: %+v baseline %d", res, len(base))
	}
	if got := readTestFile(t, hostDir, "trees/299.txt"); got != "small content" {
		t.Fatal(got)
	}
}

func TestBatchIgnoreRulesCannotBeBypassedByLaterArchiveMembers(t *testing.T) {
	hostDir, clientDir := t.TempDir(), t.TempDir()
	// The first archive entry adds an ignore file; the second attempts to add
	// something it excludes. The host must re-evaluate its ignore policy.
	writeTestFile(t, clientDir, ".gitignore", "private.txt\n")
	writeTestFile(t, clientDir, "private.txt", "sensitive")
	manifest, err := newScanner().ScanManifest(clientDir)
	if err != nil {
		t.Fatal(err)
	}
	// A client bypassing local ignore rules could submit a fabricated manifest;
	// the host must still enforce the policy after the first archive member.
	expectedHash := sha256.Sum256([]byte("sensitive"))
	batch := []batchFile{
		{Path: ".gitignore", Info: manifest.Files[".gitignore"]},
		{Path: "private.txt", Info: fileInfo{Hash: hex.EncodeToString(expectedHash[:]), Size: int64(len("sensitive"))}},
	}
	peer := testPeer(t, newHost(hostDir, "secret").handler())
	if err := peer.putBatch(context.Background(), clientDir, batch); err == nil {
		t.Fatal("ignored second archive member should be blocked")
	}
	if _, err := os.Stat(filepath.Join(hostDir, "private.txt")); !os.IsNotExist(err) {
		t.Fatal("ignored file was transferred")
	}
}
