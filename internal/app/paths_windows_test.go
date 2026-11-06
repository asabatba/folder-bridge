//go:build windows

package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"unsafe"
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
func shortPathForTest(t *testing.T, path string) string {
	t.Helper()
	input, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	getShortPath := syscall.NewLazyDLL("kernel32.dll").NewProc("GetShortPathNameW")
	size, _, callErr := getShortPath.Call(uintptr(unsafe.Pointer(input)), 0, 0)
	if size == 0 {
		t.Fatalf("GetShortPathNameW size: %v", callErr)
	}
	buffer := make([]uint16, size)
	n, _, callErr := getShortPath.Call(uintptr(unsafe.Pointer(input)), uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)))
	if n == 0 || n >= uintptr(len(buffer)) {
		t.Fatalf("GetShortPathNameW: %v", callErr)
	}
	return syscall.UTF16ToString(buffer)
}

func TestRejectPrivateSettingsPathSpellings(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "long private parent directory")
	home := filepath.Join(parent, "long private settings directory")
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	extended := func(path string) string {
		if strings.HasPrefix(path, `\\`) {
			return `\\?\UNC\` + strings.TrimPrefix(path, `\\`)
		}
		return `\\?\` + path
	}
	cases := []struct{ name, folder, settings string }{
		{"extended selected home", extended(home), home},
		{"extended settings home", home, extended(home)},
		{"extended selected parent", extended(parent), home},
		{"extended settings under parent", parent, extended(home)},
	}
	shortHome, shortParent := shortPathForTest(t, home), shortPathForTest(t, parent)
	if !strings.EqualFold(shortHome, home) && !strings.EqualFold(shortParent, parent) {
		cases = append(cases,
			struct{ name, folder, settings string }{"short selected home", shortHome, home},
			struct{ name, folder, settings string }{"short settings home", home, shortHome},
			struct{ name, folder, settings string }{"short selected parent", shortParent, home},
			struct{ name, folder, settings string }{"short settings under parent", parent, shortHome},
		)
	} else {
		t.Log("8.3 aliases unavailable on this volume; extended-path cases still run")
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := validateFolder(tc.folder, tc.settings); err == nil {
				t.Fatal("selected private settings directory through alternate path spelling")
			} else if !strings.Contains(err.Error(), "private settings") {
				t.Fatalf("unexpected rejection instead of private-settings guard: %v", err)
			}
		})
	}
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
