//go:build windows

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

// Exercise actual Win32 MoveFileW semantics on a Windows CI runner.
func TestWindowsMoveDoesNotOverwrite(t *testing.T) {
	root := t.TempDir()
	from := filepath.Join(root, "original.txt")
	to := filepath.Join(root, "destination.txt")
	if err := os.WriteFile(from, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, []byte("keep me"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := moveFileNoReplace(from, to); err == nil {
		t.Fatal("MoveFileW overwrote an existing file")
	}
	existing, err := os.ReadFile(to)
	if err != nil {
		t.Fatal(err)
	}
	if string(existing) != "keep me" {
		t.Fatal("destination modified")
	}
	old, err := os.ReadFile(from)
	if err != nil || string(old) != "original" {
		t.Fatal("source modified")
	}
	sum := sha256.Sum256([]byte("original"))
	if err := moveUnchanged(root, "original.txt", "nested/moved.txt", hex.EncodeToString(sum[:])); err != nil {
		t.Fatal(err)
	}
	moved, err := os.ReadFile(filepath.Join(root, "nested", "moved.txt"))
	if err != nil || string(moved) != "original" {
		t.Fatal("move failed")
	}
	if _, err := os.Stat(from); !os.IsNotExist(err) {
		t.Fatalf("source not removed: %v", err)
	}
}
