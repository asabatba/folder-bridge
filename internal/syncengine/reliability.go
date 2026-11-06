package syncengine

import (
	"context"
	"errors"
	"fmt"
	"folderbridge/internal/fileops"
	"io"
	"io/fs"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// retryTransfer retries only errors consistent with an interrupted connection or
// temporary server outage. It never retries conflict, authentication, or checksum
// failures. The operation must be independently safe to replay.
func retryTransfer(ctx context.Context, operation func() error) error {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err = operation()
		if err == nil || !transientTransferError(err) || attempt == 2 {
			return err
		}
		pause := time.Duration(200*(1<<attempt)) * time.Millisecond
		timer := time.NewTimer(pause)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return err
}

func transientTransferError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	var status *httpStatusError
	if errors.As(err, &status) {
		return status.Status == 408 || status.Status == 429 || status.Status >= 500
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		// A URL error can wrap a TLS pin or certificate verification failure.
		// Those are permanent and must not be retried.
		return errors.Is(urlErr.Err, syscall.ECONNRESET) || errors.Is(urlErr.Err, syscall.ECONNREFUSED) || errors.Is(urlErr.Err, syscall.EPIPE)
	}
	return false
}

const stalePartAge = 24 * time.Hour

// cleanupStaleParts is intentionally conservative: it only deletes regular
// .fb-part-* files older than 24 hours. It never follows directory links or
// touches recently created files (which may belong to an active transfer).
// These names are reserved and are always excluded from synchronization.
func cleanupStaleParts(root string, now time.Time) (int, error) {
	if err := fileops.CheckSyncRoot(root); err != nil {
		return 0, err
	}
	cleaned := 0
	err := filepath.WalkDir(root, func(full string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() || !strings.HasPrefix(entry.Name(), ".fb-part-") {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if now.Sub(info.ModTime()) < stalePartAge {
			return nil
		}
		if err := os.Remove(full); err != nil {
			return fmt.Errorf("remove stale partial %s: %w", full, err)
		}
		cleaned++
		return nil
	})
	return cleaned, err
}
