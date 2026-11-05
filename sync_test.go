package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeTestFile(t *testing.T, root, name, data string) {
	t.Helper()
	target := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}
}
func readTestFile(t *testing.T, root, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
func TestEndToEndSync(t *testing.T) {
	hostDir := t.TempDir()
	clientDir := t.TempDir()
	secret := "012345678901234567890123456789012345678901234567"
	srv := httptest.NewTLSServer(newHost(hostDir, secret).handler())
	defer srv.Close()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	pin := sha256.Sum256(srv.TLS.Certificates[0].Certificate[0])
	peer := newPeer(Invite{Addr: u.Host, Token: secret, Fingerprint: hex.EncodeToString(pin[:])})
	defer peer.Close()
	base := map[string]string{}
	scanner := NewScanner()
	step := func() {
		t.Helper()
		var err error
		base, _, err = syncFolders(context.Background(), clientDir, peer, base, scanner, false, func(s string) { t.Log(s) })
		if err != nil {
			t.Fatal(err)
		}
	}
	writeTestFile(t, hostDir, "sub/host.txt", "from host")
	writeTestFile(t, clientDir, "local.txt", "from client")
	step()
	if got := readTestFile(t, clientDir, "sub/host.txt"); got != "from host" {
		t.Fatal(got)
	}
	if got := readTestFile(t, hostDir, "local.txt"); got != "from client" {
		t.Fatal(got)
	}
	// Client modification flows to host.
	writeTestFile(t, clientDir, "local.txt", "new client contents")
	step()
	if got := readTestFile(t, hostDir, "local.txt"); got != "new client contents" {
		t.Fatal(got)
	}
	// Host modification flows to client.
	writeTestFile(t, hostDir, "sub/host.txt", "new host contents")
	step()
	if got := readTestFile(t, clientDir, "sub/host.txt"); got != "new host contents" {
		t.Fatal(got)
	}
	// Delete on client flows to host.
	if err := os.Remove(filepath.Join(clientDir, "local.txt")); err != nil {
		t.Fatal(err)
	}
	step()
	if _, err := os.Stat(filepath.Join(hostDir, "local.txt")); !os.IsNotExist(err) {
		t.Fatalf("remote deletion missing: %v", err)
	}
	// Delete on host flows to client.
	if err := os.Remove(filepath.Join(hostDir, "sub/host.txt")); err != nil {
		t.Fatal(err)
	}
	step()
	if _, err := os.Stat(filepath.Join(clientDir, "sub/host.txt")); !os.IsNotExist(err) {
		t.Fatalf("local deletion missing: %v", err)
	}
	// Both sides add a different file with the same name -> conflict copy.
	writeTestFile(t, clientDir, "notes.txt", "client version")
	writeTestFile(t, hostDir, "notes.txt", "host version")
	step()
	if got := readTestFile(t, hostDir, "notes.txt"); got != "client version" {
		t.Fatal(got)
	}
	files, err := os.ReadDir(clientDir)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, f := range files {
		if strings.Contains(f.Name(), "conflict-from-other-PC") {
			count++
			if got := readTestFile(t, clientDir, f.Name()); got != "host version" {
				t.Fatal(got)
			}
		}
	}
	if count != 1 {
		t.Fatalf("expected one conflict copy, found %d", count)
	}
	step()
	remoteFiles, err := os.ReadDir(hostDir)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range remoteFiles {
		if strings.Contains(f.Name(), "conflict-from-other-PC") {
			found = true
		}
	}
	if !found {
		t.Fatal("conflict backup did not propagate")
	}
	// Modification vs deletion: keep the modified version.
	writeTestFile(t, clientDir, "notes.txt", "local changed")
	if err := os.Remove(filepath.Join(hostDir, "notes.txt")); err != nil {
		t.Fatal(err)
	}
	step()
	if got := readTestFile(t, hostDir, "notes.txt"); got != "local changed" {
		t.Fatal(got)
	}
}

func TestPathValidation(t *testing.T) {
	root := t.TempDir()
	for _, bad := range []string{"", "../secret", "nested/../secret", "/abs", "C:\\windows\\secret", "nested\\windows", "a//b"} {
		if _, err := checkedPath(root, bad); err == nil {
			t.Errorf("allowed unsafe path %q", bad)
		}
	}
	p, err := checkedPath(root, "safe/dir/hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p, root) {
		t.Fatal(p)
	}
}

func TestBadCredentials(t *testing.T) {
	srv := httptest.NewTLSServer(newHost(t.TempDir(), "expected").handler())
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	pin := sha256.Sum256(srv.TLS.Certificates[0].Certificate[0])
	peer := newPeer(Invite{Addr: u.Host, Token: "incorrect", Fingerprint: hex.EncodeToString(pin[:])})
	defer peer.Close()
	_, err := peer.manifest(context.Background())
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("expected unauthorized, got %v", err)
	}
	peer2 := newPeer(Invite{Addr: u.Host, Token: "expected", Fingerprint: strings.Repeat("0", 64)})
	defer peer2.Close()
	_, err = peer2.manifest(context.Background())
	if err == nil || !strings.Contains(err.Error(), "fingerprint") {
		t.Fatalf("expected TLS pin failure, got %v", err)
	}
}

func TestBulkDeleteRequiresApproval(t *testing.T) {
	hostDir, clientDir := t.TempDir(), t.TempDir()
	secret := "012345678901234567890123456789012345678901234567"
	srv := httptest.NewTLSServer(newHost(hostDir, secret).handler())
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	pin := sha256.Sum256(srv.TLS.Certificates[0].Certificate[0])
	peer := newPeer(Invite{Addr: u.Host, Token: secret, Fingerprint: hex.EncodeToString(pin[:])})
	defer peer.Close()
	for i := 0; i < 25; i++ {
		writeTestFile(t, hostDir, fmt.Sprintf("file-%02d.txt", i), "content")
	}
	scanner := NewScanner()
	base := map[string]string{}
	var err error
	base, _, err = syncFolders(context.Background(), clientDir, peer, base, scanner, false, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 25; i++ {
		if err := os.Remove(filepath.Join(clientDir, fmt.Sprintf("file-%02d.txt", i))); err != nil {
			t.Fatal(err)
		}
	}
	_, _, err = syncFolders(context.Background(), clientDir, peer, base, scanner, false, func(string) {})
	if err == nil || !strings.Contains(err.Error(), "Safety stop") {
		t.Fatalf("expected safety stop, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(hostDir, "file-00.txt")); err != nil {
		t.Fatal("host file deleted without approval", err)
	}
	base, _, err = syncFolders(context.Background(), clientDir, peer, base, scanner, true, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if len(base) != 0 {
		t.Fatalf("baseline still has %d files", len(base))
	}
	if _, err := os.Stat(filepath.Join(hostDir, "file-00.txt")); !os.IsNotExist(err) {
		t.Fatalf("approved deletion not propagated: %v", err)
	}
}

func TestStagedWriteDoesNotClobberConcurrentChanges(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "target.txt")
	writeTestFile(t, root, "target.txt", "modified after scan")
	sum := sha256.Sum256([]byte("incoming"))
	expectedHash := hex.EncodeToString(sum[:])
	expectedOldHash := "this-was-the-old-hash"
	err := stagedWriteGuarded(path, strings.NewReader("incoming"), expectedHash, time.Time{}, &expectedOldHash)
	if err == nil {
		t.Fatal("expected conflict guard to reject overwrite")
	}
	if got := readTestFile(t, root, "target.txt"); got != "modified after scan" {
		t.Fatalf("data lost: %q", got)
	}
}
