// Package fileops owns guarded filesystem operations shared by application
// configuration and synchronization. It has no knowledge of peers or app state.
package fileops

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// HashFile returns a SHA-256 digest of a regular file's contents.
func HashFile(filename string) (string, error) {
	f, err := os.Open(filename)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// HashStableFile refuses a file changed since the supplied scan metadata.
func HashStableFile(filename string, before os.FileInfo) (string, error) {
	result, err := HashFile(filename)
	if err != nil {
		return "", err
	}
	after, err := os.Stat(filename)
	if err != nil {
		return "", err
	}
	if after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return "", errors.New("file was modified during scan; retrying on next cycle")
	}
	return result, nil
}

// CurrentHash returns an empty hash for an absent file; other unsupported paths
// and mutations during hashing are errors, not absence.
func CurrentHash(filename string) (string, error) {
	info, err := os.Lstat(filename)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("not a regular file")
	}
	return HashStableFile(filename, info)
}

// CheckExpected enforces the manifest's expected content or expected absence.
func CheckExpected(filename, expected string) error {
	got, err := CurrentHash(filename)
	if err != nil {
		return err
	}
	if got != expected {
		return errors.New("destination changed since manifest")
	}
	return nil
}

// StagedWriteGuarded streams and verifies a file before publishing it. A non-nil
// originalHash guards against concurrent changes; an empty hash expects absence.
func StagedWriteGuarded(dest string, data io.Reader, expectedHash string, mtime time.Time, originalHash *string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".fb-part-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	h := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(tmp, h), data)
	if copyErr != nil {
		tmp.Close()
		return copyErr
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != expectedHash {
		tmp.Close()
		return fmt.Errorf("checksum mismatch: expected %s, got %s", expectedHash, got)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if !mtime.IsZero() {
		if err := os.Chtimes(tmpName, mtime, mtime); err != nil {
			return err
		}
	}
	return installStagedFile(tmpName, dest, originalHash)
}

// WriteAtomically flushes private settings/index/baseline bytes before replacing
// the previous file. It also supports PEM certificate bytes, not just JSON.
func WriteAtomically(filename string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(filename), ".fb-part-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err = tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return installStagedFile(tmpName, filename, nil)
}
