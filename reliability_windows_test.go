//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestWindowsLockedDestinationKeepsOriginal(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "locked.txt")
	writeTestFile(t, root, "locked.txt", "old contents")
	ptr, err := syscall.UTF16PtrFromString(dest)
	if err != nil {
		t.Fatal(err)
	}
	// Do not grant FILE_SHARE_DELETE: MoveFileExW must reject replacement.
	handle, err := syscall.CreateFile(ptr, syscall.GENERIC_READ, syscall.FILE_SHARE_READ, nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	old := digest("old contents")
	err = stagedWriteGuarded(dest, strings.NewReader("new contents"), digest("new contents"), time.Time{}, &old)
	syscall.CloseHandle(handle)
	if err == nil {
		t.Fatal("replaced locked file despite denied delete access")
	}
	if got := readTestFile(t, root, "locked.txt"); got != "old contents" {
		t.Fatalf("data loss: %s", got)
	}
	if err := stagedWriteGuarded(dest, strings.NewReader("new contents"), digest("new contents"), time.Time{}, &old); err != nil {
		t.Fatal(err)
	}
	if got := readTestFile(t, root, "locked.txt"); got != "new contents" {
		t.Fatalf("retry failed: %s", got)
	}
	files, _ := os.ReadDir(root)
	if len(files) != 1 {
		t.Fatalf("temp leak after locked-file failure: %v", files)
	}
}
