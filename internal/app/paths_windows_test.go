//go:build windows

package app

import (
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
func TestRejectPrivateSettingsJunctionAlias(t *testing.T) {
	parent := t.TempDir()
	home := filepath.Join(parent, "settings")
	writeTestFile(t, home, "config.json", "PRIVATE-PLACEHOLDER")
	alias := filepath.Join(t.TempDir(), "alias")
	createTestJunction(t, alias, parent)
	if _, err := validateFolder(filepath.Join(alias, "settings"), home); err == nil {
		t.Fatal("private settings alias accepted")
	}
	if _, err := validateFolder(parent, filepath.Join(alias, "settings")); err == nil {
		t.Fatal("real parent containing aliased private settings accepted")
	}
}
