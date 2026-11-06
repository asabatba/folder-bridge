package syncengine

import (
	"compress/gzip"
	"context"
	"path"
	"strings"
	"sync"
	"time"
)

// All tuning is per process and advisory. The protocol, size and integrity
// limits stay unchanged. Two workers are enough to overlap LAN I/O with disk
// and gzip without overwhelming a laptop.
type transferTuner struct {
	mu    sync.Mutex
	speed float64 // exponentially smoothed uncompressed bytes/sec
}

func (t *transferTuner) parameters() (int64, int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	switch {
	case t.speed >= 24<<20:
		return 8 << 20, 3
	case t.speed >= 4<<20:
		return 4 << 20, 2
	case t.speed > 0 && t.speed < 128<<10:
		return 1 << 20, 1
	default:
		return 2 << 20, 2
	}
}

func (t *transferTuner) observe(bytes int64, elapsed time.Duration) {
	if bytes <= 0 || elapsed < 20*time.Millisecond {
		return
	}
	rate := float64(bytes) / elapsed.Seconds()
	t.mu.Lock()
	if t.speed == 0 {
		t.speed = rate
	} else {
		t.speed = .7*t.speed + .3*rate
	}
	t.mu.Unlock()
}

// gzip level 0 still uses the existing tar.gz framing, making this choice
// backward compatible with v2.1 peers while avoiding wasted CPU time.
func compressionForBatch(files []batchFile) int {
	var total, compressed int64
	for _, f := range files {
		total += f.Info.Size
		switch strings.ToLower(path.Ext(f.Path)) {
		case ".zip", ".gz", ".xz", ".zst", ".7z", ".rar", ".jpg", ".jpeg", ".png", ".webp", ".gif", ".avif", ".heic", ".mp4", ".mkv", ".mov", ".webm", ".mp3", ".aac", ".ogg", ".flac", ".pdf", ".woff2":
			compressed += f.Info.Size
		}
	}
	if total > 0 && compressed*10 >= total*7 {
		return gzip.NoCompression
	}
	return gzip.BestSpeed
}

type batchRunResult struct {
	chunk     []batchFile
	successes []string
	err       error
}

// Runs independent batches concurrently; bookkeeping and baseline changes
// happen only after joining workers, avoiding map races and partial state.
func (p *peer) runBatches(ctx context.Context, root string, chunks [][]batchFile, upload bool) []batchRunResult {
	_, workers := p.tuner.parameters()
	if workers < 1 {
		workers = 1
	}
	outcomes := make([]batchRunResult, 0, len(chunks))
	for i := 0; i < len(chunks) && ctx.Err() == nil; i += workers {
		end := i + workers
		if end > len(chunks) {
			end = len(chunks)
		}
		wave := make([]batchRunResult, end-i)
		var wg sync.WaitGroup
		for j := i; j < end; j++ {
			wg.Add(1)
			go func(pos int) {
				defer wg.Done()
				chunk := chunks[pos]
				entry := &wave[pos-i]
				entry.chunk = chunk
				start := time.Now()
				if upload {
					entry.err = retryTransfer(ctx, func() error { return p.putBatch(ctx, root, chunk) })
					if entry.err == nil {
						for _, item := range chunk {
							entry.successes = append(entry.successes, item.Path)
						}
					}
				} else {
					entry.successes, entry.err = p.getBatch(ctx, root, chunk)
				}
				if entry.err == nil {
					var size int64
					for _, item := range chunk {
						size += item.Info.Size
					}
					p.tuner.observe(size, time.Since(start))
				}
			}(j)
		}
		wg.Wait()
		outcomes = append(outcomes, wave...)
		interrupted := false
		for _, outcome := range wave {
			if outcome.err != nil && transientTransferError(outcome.err) {
				interrupted = true
			}
		}
		if interrupted {
			break
		}
	}
	return outcomes
}
