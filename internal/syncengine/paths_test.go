package syncengine

import (
	"folderbridge/internal/fileops"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestInvalidRootsAreNotEmptyManifests(t *testing.T) {
	parent := t.TempDir()
	writeTestFile(t, parent, "file.txt", "not a directory")
	for _, root := range []string{filepath.Join(parent, "missing"), filepath.Join(parent, "file.txt")} {
		if _, err := newScanner().ScanManifest(root); err == nil {
			t.Errorf("root %q returned a successful scan", root)
		}
		if _, err := fileops.CheckedPath(root, "new.txt"); err == nil {
			t.Errorf("root %q accepted transfer", root)
		}
	}
}
func TestScannerRejectsSymbolicLinkRoots(t *testing.T) {
	target := t.TempDir()
	writeTestFile(t, target, "child/important.txt", "keep me")
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(target, alias); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symbolic-link privilege unavailable; junctions have separate coverage: %v", err)
		}
		t.Fatal(err)
	}
	for _, root := range []string{alias, filepath.Join(alias, "child")} {
		if _, err := newScanner().ScanManifest(root); err == nil {
			t.Errorf("scanned linked path %q", root)
		}
	}
}
