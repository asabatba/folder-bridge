package fileops

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// MoveUnchanged verifies the source hash, refuses an existing destination, and
// atomically moves the file on the same filesystem. This is intentionally a
// rename-only optimization: the normal transfer path handles all other cases.
func MoveUnchanged(root, from, to, expected string) error {
	if from == to || strings.EqualFold(from, to) {
		return errors.New("case-only or identical rename is not supported")
	}
	source, err := CheckedPath(root, from)
	if err != nil {
		return err
	}
	dest, err := CheckedPath(root, to)
	if err != nil {
		return err
	}
	if err := CheckExpected(source, expected); err != nil {
		return fmt.Errorf("source changed: %w", err)
	}
	if err := CheckExpected(dest, ""); err != nil {
		return fmt.Errorf("destination occupied: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return err
	}
	// Revalidate after creating parent directories (a changed symlink/junction
	// must not redirect the new path outside the sync root).
	if _, err := CheckedPath(root, to); err != nil {
		return err
	}
	// A Windows editor or antivirus scanner may temporarily deny a rename.
	// Retry only transient sharing/lock violations; occupied destinations and
	// stale source hashes must not be retried as conflicts.
	var moveErr error
	for attempt := 0; attempt < 3; attempt++ {
		moveErr = moveFileNoReplace(source, dest)
		if moveErr == nil || !retryableReplaceError(moveErr) {
			return moveErr
		}
		time.Sleep(time.Duration(50<<attempt) * time.Millisecond)
	}
	return moveErr
}
