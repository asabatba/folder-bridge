package fileops

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type interruptedReader struct{ remaining int }

func (r *interruptedReader) Read(buf []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.ErrUnexpectedEOF
	}
	n := len(buf)
	if n > r.remaining {
		n = r.remaining
	}
	for i := 0; i < n; i++ {
		buf[i] = 'x'
	}
	r.remaining -= n
	return n, nil
}

func TestInterruptedWritePreservesOriginalAndCleansTemp(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "document.txt", "original and important")
	old := digest("original and important")
	err := StagedWriteGuarded(filepath.Join(root, "document.txt"), &interruptedReader{remaining: 5}, digest(strings.Repeat("x", 200)), time.Time{}, &old)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("expected interrupted stream: %v", err)
	}
	if got := readTestFile(t, root, "document.txt"); got != "original and important" {
		t.Fatalf("original overwritten: %q", got)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("leaked temp after interruption: %v", entries)
	}
	if err := StagedWriteGuarded(filepath.Join(root, "document.txt"), strings.NewReader("recovered"), digest("recovered"), time.Time{}, &old); err != nil {
		t.Fatal(err)
	}
	if got := readTestFile(t, root, "document.txt"); got != "recovered" {
		t.Fatal(got)
	}
}
func TestChecksumMismatchPreservesOriginalAndClosesTemp(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "important.txt", "old")
	old := digest("old")
	err := StagedWriteGuarded(filepath.Join(root, "important.txt"), strings.NewReader("bad data"), digest("good data"), time.Time{}, &old)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("expected checksum failure: %v", err)
	}
	if got := readTestFile(t, root, "important.txt"); got != "old" {
		t.Fatal(got)
	}
	files, err := os.ReadDir(root)
	if err != nil || len(files) != 1 {
		t.Fatalf("temporary files left behind: %v %v", files, err)
	}
}
func TestIncompleteReaderDoesNotLeakParts(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "new.txt")
	absent := ""
	err := StagedWriteGuarded(dest, io.MultiReader(bytes.NewBufferString("some"), &interruptedReader{}), digest("expected"), time.Time{}, &absent)
	if err == nil {
		t.Fatal("expected interrupted reader failure")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatal("incomplete file appeared at destination")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("leftover partial files: %v", entries)
	}
}
func TestPathValidation(t *testing.T) {
	root := t.TempDir()
	for _, bad := range []string{"", "../secret", "nested/../secret", "/abs", "C:\\windows\\secret", "nested\\windows", "a//b"} {
		if _, err := CheckedPath(root, bad); err == nil {
			t.Errorf("allowed unsafe path %q", bad)
		}
	}
	p, err := CheckedPath(root, "safe/dir/hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p, root) {
		t.Fatal(p)
	}
}
func TestStagedWriteDoesNotClobberConcurrentChanges(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "target.txt")
	writeTestFile(t, root, "target.txt", "modified after scan")
	expectedOldHash := "this-was-the-old-hash"
	err := StagedWriteGuarded(dest, strings.NewReader("incoming"), digest("incoming"), time.Time{}, &expectedOldHash)
	if err == nil {
		t.Fatal("expected conflict guard to reject overwrite")
	}
	if got := readTestFile(t, root, "target.txt"); got != "modified after scan" {
		t.Fatalf("data lost: %q", got)
	}
}
