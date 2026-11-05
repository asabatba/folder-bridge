package main

import (
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
		if _, err := NewScanner().ScanManifest(root); err == nil {
			t.Errorf("scanned linked path %q", root)
		}
		if _, err := checkedPath(root, "new.txt"); err == nil {
			t.Errorf("transfer through linked path %q", root)
		}
	}
}

func TestInvalidRootsAreNotEmptyManifests(t *testing.T) {
	parent := t.TempDir()
	writeTestFile(t, parent, "file.txt", "not a directory")
	for _, root := range []string{filepath.Join(parent, "missing"), filepath.Join(parent, "file.txt")} {
		if _, err := NewScanner().ScanManifest(root); err == nil {
			t.Errorf("root %q returned a successful scan", root)
		}
		if _, err := checkedPath(root, "new.txt"); err == nil {
			t.Errorf("root %q accepted transfer", root)
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
	manifest, err := NewScanner().ScanManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Files["hello.txt"].Hash != digest("ordinary contents") {
		t.Fatal("ordinary file not scanned")
	}
	if _, err := checkedPath(root, "new/file.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := validateFolder(home, home); err == nil {
		t.Fatal("selected private settings directory")
	}
}
