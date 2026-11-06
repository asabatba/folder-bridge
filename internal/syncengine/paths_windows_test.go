//go:build windows

package syncengine

import (
	"context"
	"folderbridge/internal/fileops"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func createTestJunction(t *testing.T, link, target string) {
	t.Helper()
	out, err := exec.Command("cmd.exe", "/c", "mklink", "/J", link, target).CombinedOutput()
	if err != nil {
		t.Fatalf("create junction: %v: %s", err, out)
	}
	t.Cleanup(func() {
		if err := os.Remove(link); err != nil && !os.IsNotExist(err) {
			t.Errorf("remove junction: %v", err)
		}
	})
}
func TestScannerRejectsJunctionRoots(t *testing.T) {
	target := t.TempDir()
	writeTestFile(t, target, "child/important.txt", "keep me")
	alias := filepath.Join(t.TempDir(), "alias")
	createTestJunction(t, alias, target)
	for _, root := range []string{alias, filepath.Join(alias, "child")} {
		if _, err := newScanner().ScanManifest(root); err == nil {
			t.Errorf("scanned junction path %q", root)
		}
		if _, err := fileops.CheckedPath(root, "new.txt"); err == nil {
			t.Errorf("transfer accepted junction path %q", root)
		}
	}
}
func TestJunctionReplacementPreservesSyncFiles(t *testing.T) {
	for _, side := range []string{"client", "host"} {
		t.Run(side, func(t *testing.T) {
			f := newSyncFixture(t)
			writeTestFile(t, f.clientDir, "important.txt", "important contents")
			f.sync(t)
			root, peerRoot := f.clientDir, f.hostDir
			if side == "host" {
				root, peerRoot = f.hostDir, f.clientDir
			}
			relocated := filepath.Join(t.TempDir(), "relocated")
			if err := os.Rename(root, relocated); err != nil {
				t.Fatal(err)
			}
			createTestJunction(t, root, relocated)
			next, _, err := syncFolders(context.Background(), f.clientDir, f.peer, f.base, f.scanner, false, func(string) {})
			if err == nil {
				t.Error("sync succeeded after junction replacement")
			}
			for _, dir := range []string{relocated, peerRoot} {
				if got := readTestFile(t, dir, "important.txt"); got != "important contents" {
					t.Fatalf("lost contents: %q", got)
				}
			}
			if next["important.txt"] != digest("important contents") {
				t.Error("baseline changed")
			}
		})
	}
}
