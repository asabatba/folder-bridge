package syncengine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func digest(data string) string {
	sum := sha256.Sum256([]byte(data))
	return hex.EncodeToString(sum[:])
}

func TestCrashRecoveryCleanupIgnoresActivePartials(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "document.txt", "keep")
	writeTestFile(t, root, ".fb-part-orphan", "incomplete")
	writeTestFile(t, root, "sub/.fb-part-active", "currently transferring")
	writeTestFile(t, root, "sub/.fb-part-orphan2", "incomplete")
	writeTestFile(t, root, ".git/.fb-part-ignore", "inside git")
	now := time.Now()
	past := now.Add(-stalePartAge - time.Minute)
	for _, name := range []string{".fb-part-orphan", "sub/.fb-part-orphan2", ".git/.fb-part-ignore"} {
		f := filepath.Join(root, filepath.FromSlash(name))
		if err := os.Chtimes(f, past, past); err != nil {
			t.Fatal(err)
		}
	}
	n, err := cleanupStaleParts(root, now)
	if err != nil || n != 2 {
		t.Fatalf("removed %d: %v", n, err)
	}
	for _, name := range []string{"document.txt", "sub/.fb-part-active", ".git/.fb-part-ignore"} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(name))); err != nil {
			t.Fatalf("should preserve %q: %v", name, err)
		}
	}
	snap, err := newScanner().ScanManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Files) != 1 {
		t.Fatalf("part files unexpectedly synced: %v", snap.Files)
	}
}

func TestBaselineRewrittenAtomically(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "baseline.json")
	for i := 0; i < 6; i++ {
		content := map[string]string{"item.txt": digest(fmt.Sprintf("round %d", i))}
		if err := saveBaseline(filename, content); err != nil {
			t.Fatal(err)
		}
		got, err := loadBaseline(filename)
		if err != nil || got["item.txt"] != content["item.txt"] {
			t.Fatalf("round %d: %v %v", i, got, err)
		}
	}
	files, err := os.ReadDir(filepath.Dir(filename))
	if err != nil || len(files) != 1 {
		t.Fatalf("unexpected temp files %v %v", files, err)
	}
}

func TestTransientRetriesAndPermanentConflict(t *testing.T) {
	var count atomic.Int32
	err := retryTransfer(context.Background(), func() error {
		if count.Add(1) <= 2 {
			return &httpStatusError{Status: http.StatusServiceUnavailable, Detail: "busy"}
		}
		return nil
	})
	if err != nil || count.Load() != 3 {
		t.Fatalf("retries=%d err=%v", count.Load(), err)
	}
	count.Store(0)
	err = retryTransfer(context.Background(), func() error {
		count.Add(1)
		return &httpStatusError{Status: http.StatusConflict, Detail: "changed"}
	})
	if err == nil || count.Load() != 1 {
		t.Fatalf("permanent conflict repeated %d times: %v", count.Load(), err)
	}
}

func TestCancelledRetryDoesNotWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	err := retryTransfer(ctx, func() error {
		calls++
		cancel()
		return io.ErrUnexpectedEOF
	})
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestReconciliationAfterLostResponse(t *testing.T) {
	hostDir, clientDir := t.TempDir(), t.TempDir()
	// Simulate a completed upload whose response was lost and whose baseline
	// wasn't persisted before the application exited.
	writeTestFile(t, clientDir, "note.txt", "latest")
	writeTestFile(t, hostDir, "note.txt", "latest")
	peer := testPeer(t, newHost(hostDir, "secret").handler())
	base, res, err := syncFolders(context.Background(), clientDir, peer, map[string]string{}, newScanner(), false, func(string) {})
	if err != nil || res.Uploaded != 0 || res.Downloaded != 0 || base["note.txt"] != digest("latest") {
		t.Fatalf("restart recovery: %v %+v %v", base, res, err)
	}
}

func TestInterruptedBatchIsRecoverableOnNextScan(t *testing.T) {
	hostDir, clientDir := t.TempDir(), t.TempDir()
	for i := 0; i < 20; i++ {
		writeTestFile(t, clientDir, fmt.Sprintf("file-%02d", i), "contents")
	}
	host := newHost(hostDir, "secret")
	var interrupted atomic.Bool
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/batch/put" && !interrupted.Swap(true) {
			w.WriteHeader(http.StatusServiceUnavailable)
			io.Copy(io.Discard, io.LimitReader(r.Body, 1024))
			return
		}
		host.handler().ServeHTTP(w, r)
	})
	peer := testPeer(t, handler)
	base, res, err := syncFolders(context.Background(), clientDir, peer, map[string]string{}, newScanner(), false, func(string) {})
	if err != nil || res.Uploaded != 20 || res.BatchUploads != 1 || len(base) != 20 {
		t.Fatalf("batch recovery: %v %+v %v", base, res, err)
	}
	_, res, err = syncFolders(context.Background(), clientDir, peer, base, newScanner(), false, func(string) {})
	if err != nil || res.Uploaded != 0 || res.Downloaded != 0 {
		t.Fatalf("repeat after recovery: %+v %v", res, err)
	}
}
