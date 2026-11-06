package app

import (
	"folderbridge/internal/fileops"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestRejectSymbolicLinkFolderPaths(t *testing.T) {
	target := t.TempDir()
	writeTestFile(t, target, "child/important.txt", "keep me")
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(target, alias); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symbolic-link privilege unavailable; junctions have separate Windows coverage: %v", err)
		}
		t.Fatal(err)
	}
	for _, root := range []string{alias, filepath.Join(alias, "child")} {
		if _, err := validateFolder(root, t.TempDir()); err == nil {
			t.Errorf("selected linked path %q", root)
		}
		if _, err := fileops.CheckedPath(root, "new.txt"); err == nil {
			t.Errorf("transfer through linked path %q", root)
		}
	}
}
func TestOrdinaryFolderSelectionAndScan(t *testing.T) {
	home := t.TempDir()
	root, err := validateFolder(filepath.Join(t.TempDir(), "new", "shared"), home)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, root, "hello.txt", "ordinary contents")
	if _, err := fileops.CheckedPath(root, "new/file.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := validateFolder(home, home); err == nil {
		t.Fatal("selected private settings directory")
	}
}
