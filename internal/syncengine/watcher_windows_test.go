//go:build windows

package syncengine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWindowsNativeDirectoryWatcher(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan string, 32)
	done := make(chan error, 1)
	go func() {
		done <- watchFolder(ctx, root, func(path string) {
			select {
			case got <- path:
			default:
			}
		})
	}()
	// Windows has no ready callback on the synchronous read operation.
	time.Sleep(250 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(root, "watch-test.txt"), []byte("works"), 0600); err != nil {
		t.Fatal(err)
	}
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	for {
		select {
		case name := <-got:
			if name == "watch-test.txt" {
				cancel()
				return
			}
		case err := <-done:
			t.Fatalf("watcher ended: %v", err)
		case <-timeout.C:
			t.Fatal("file change did not generate notification")
		}
	}
}
