//go:build windows

package main

import (
	"context"
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

func TestRejectJunctionFolderPaths(t *testing.T) {
	target := t.TempDir()
	writeTestFile(t, target, "child/important.txt", "keep me")
	alias := filepath.Join(t.TempDir(), "alias")
	createTestJunction(t, alias, target)
	for _, root := range []string{alias, filepath.Join(alias, "child")} {
		t.Run(root, func(t *testing.T) {
			if _, err := validateFolder(root, t.TempDir()); err == nil {
				t.Error("folder selection accepted junction path")
			}
			if _, err := NewScanner().ScanManifest(root); err == nil {
				t.Error("scan accepted junction path")
			}
			if _, err := checkedPath(root, "new.txt"); err == nil {
				t.Error("transfer accepted junction path")
			}
		})
	}
	missing := filepath.Join(alias, "must-not-be-created")
	if _, err := validateFolder(missing, t.TempDir()); err == nil {
		t.Error("accepted missing folder under junction")
	}
	if _, err := os.Stat(filepath.Join(target, "must-not-be-created")); !os.IsNotExist(err) {
		t.Errorf("validation created a folder through junction: %v", err)
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

func TestRejectPrivateSettingsJunctionAlias(t *testing.T) {
	parent := t.TempDir()
	home := filepath.Join(parent, "settings")
	writeTestFile(t, home, "config.json", "PRIVATE-PLACEHOLDER")
	alias := filepath.Join(t.TempDir(), "alias")
	createTestJunction(t, alias, parent)
	if _, err := validateFolder(filepath.Join(alias, "settings"), home); err == nil {
		t.Fatal("private settings alias accepted")
	}
	// Settings may themselves be configured through an AppData alias.
	if _, err := validateFolder(parent, filepath.Join(alias, "settings")); err == nil {
		t.Fatal("real parent containing aliased private settings accepted")
	}
}
