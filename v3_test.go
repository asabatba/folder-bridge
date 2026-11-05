package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPersistentIndexRestartAndInvalidation(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	for i := 0; i < 20; i++ {
		writeTestFile(t, root, "folder/"+string(rune('a'+i))+".txt", "sample content")
	}
	first := NewPersistentScanner(root, home)
	if _, err := first.ScanManifest(root); err != nil {
		t.Fatal(err)
	}
	if got := first.Stats(); got.Hashed != 20 || got.Reused != 0 {
		t.Fatalf("initial scan: %+v", got)
	}
	if _, err := os.Stat(indexFilename(home, root)); err != nil {
		t.Fatal(err)
	}

	restarted := NewPersistentScanner(root, home)
	if _, err := restarted.ScanManifest(root); err != nil {
		t.Fatal(err)
	}
	if got := restarted.Stats(); got.Restored != 20 || got.Hashed != 0 || got.Reused != 20 {
		t.Fatalf("restart: %+v", got)
	}

	// A change preserving size and mtime is still rehashed after an event.
	changed := filepath.Join(root, "folder", "a.txt")
	before, err := os.Stat(changed)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(changed, []byte("different data"), 0600); err != nil {
		t.Fatal(err)
	}
	// Keep exact previous byte count too.
	if err := os.WriteFile(changed, []byte("sample CONTENT"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(changed, before.ModTime(), before.ModTime()); err != nil {
		t.Fatal(err)
	}
	restarted.Invalidate("folder/a.txt", false)
	manifest, err := restarted.ScanManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := restarted.Stats(); got.Hashed != 1 || got.Reused != 19 {
		t.Fatalf("invalidation: %+v", got)
	}
	if manifest.Files["folder/a.txt"].Hash == first.cache["folder/a.txt"].Hash {
		t.Fatal("same-size, same-mtime edit missed")
	}

	if err := os.Remove(changed); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.ScanManifest(root); err != nil {
		t.Fatal(err)
	}
	third := NewPersistentScanner(root, home)
	if _, exists := third.cache["folder/a.txt"]; exists {
		t.Fatal("deleted file persisted in index")
	}
}

func TestBrokenIndexIsOnlyAnOptimization(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	writeTestFile(t, root, "keep.txt", "valuable data")
	if err := os.WriteFile(indexFilename(home, root), []byte(`{broken json`), 0600); err != nil {
		t.Fatal(err)
	}
	scanner := NewPersistentScanner(root, home)
	manifest, err := scanner.ScanManifest(root)
	if err != nil || len(manifest.Files) != 1 || scanner.Stats().Hashed != 1 {
		t.Fatalf("broken cache not recovered: %+v %v", manifest, err)
	}
}

func TestHostLongPollWakesOnFileChange(t *testing.T) {
	root := t.TempDir()
	h := newHost(root, "secret")
	called := make(chan struct{}, 1)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v3/changes" {
			select {
			case called <- struct{}{}:
			default:
			}
		}
		h.handler().ServeHTTP(w, r)
	})
	p := testPeer(t, root, handler)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	results := make(chan struct {
		seq uint64
		err error
	}, 1)
	go func() {
		n, err := p.waitForChanges(ctx, 1)
		results <- struct {
			seq uint64
			err error
		}{n, err}
	}()
	select {
	case <-called:
	case <-ctx.Done():
		t.Fatal("poll never reached host")
	}
	h.signalChange()
	select {
	case r := <-results:
		if r.err != nil || r.seq != 2 {
			t.Fatalf("notification: %d %v", r.seq, r.err)
		}
	case <-ctx.Done():
		t.Fatal("notification did not wake long poll")
	}
}

func TestAdaptiveTransferChoices(t *testing.T) {
	list := []BatchFile{{Path: "photo.jpg", Info: FileInfo{Size: 1200}}, {Path: "video.mp4", Info: FileInfo{Size: 1200}}}
	if compressionForBatch(list) != 0 {
		t.Fatal("already-compressed content should skip deflate")
	}
	list = []BatchFile{{Path: "log.txt", Info: FileInfo{Size: 1200}}}
	if compressionForBatch(list) == 0 {
		t.Fatal("text should be compressed")
	}
	tuner := &transferTuner{}
	size, workers := tuner.parameters()
	if size != 2<<20 || workers != 2 {
		t.Fatalf("defaults: %d %d", size, workers)
	}
	tuner.observe(32<<20, time.Second)
	size, workers = tuner.parameters()
	if size != 8<<20 || workers != 3 {
		t.Fatalf("fast LAN: %d %d", size, workers)
	}
	batches := selectBatchChunksWithTarget(make([]BatchFile, 0), 1<<20)
	if len(batches) != 0 {
		t.Fatal("empty input yielded batch")
	}
}

func TestBatchUploadRequestsCanOverlap(t *testing.T) {
	hostRoot, clientRoot := t.TempDir(), t.TempDir()
	for i := 0; i < 300; i++ {
		writeTestFile(t, clientRoot, "batch/"+strings.Repeat("0", 3-len(intString(i)))+intString(i)+".txt", "tiny content")
	}
	h := newHost(hostRoot, "secret")
	var current, maxSeen atomic.Int64
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/batch/put" {
			n := current.Add(1)
			for {
				last := maxSeen.Load()
				if n <= last || maxSeen.CompareAndSwap(last, n) {
					break
				}
			}
			time.Sleep(70 * time.Millisecond)
			defer current.Add(-1)
		}
		h.handler().ServeHTTP(w, r)
	})
	peer := testPeer(t, hostRoot, handler)
	base, result, err := syncFolders(context.Background(), clientRoot, peer, map[string]string{}, NewScanner(), false, func(string) {})
	if err != nil || len(base) != 300 || result.Uploaded != 300 {
		t.Fatalf("batch sync: %+v %v", result, err)
	}
	if maxSeen.Load() < 2 {
		t.Fatalf("requests remained serial, max %d", maxSeen.Load())
	}
}

func intString(v int) string {
	if v == 0 {
		return "0"
	}
	b := [20]byte{}
	p := len(b)
	for v > 0 {
		p--
		b[p] = byte('0' + v%10)
		v /= 10
	}
	return string(b[p:])
}

func TestDebounceCollapsesBursts(t *testing.T) {
	ch := make(chan struct{}, 10)
	var wg sync.WaitGroup
	wg.Add(1)
	start := time.Now()
	go func() {
		defer wg.Done()
		for i := 0; i < 5; i++ {
			ch <- struct{}{}
			time.Sleep(20 * time.Millisecond)
		}
	}()
	if !debounceChanges(context.Background(), ch, 80*time.Millisecond, 500*time.Millisecond) {
		t.Fatal("debounce cancelled")
	}
	wg.Wait()
	if time.Since(start) < 90*time.Millisecond {
		t.Fatal("debounce returned before changes settled")
	}
}
