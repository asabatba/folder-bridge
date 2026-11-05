package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

type changeResponse struct {
	Sequence uint64 `json:"sequence"`
}

func (h *Host) signalChange() {
	h.changeMu.Lock()
	h.changeSeq++
	close(h.changeSignal)
	h.changeSignal = make(chan struct{})
	h.changeMu.Unlock()
}

// Long-poll avoids a full remote manifest fetch every few seconds when idle.
// Every request also completes after 25 s so a disconnected peer can recover.
func (h *Host) waitForChanges(w http.ResponseWriter, r *http.Request) {
	since, err := strconv.ParseUint(r.URL.Query().Get("since"), 10, 64)
	if err != nil {
		http.Error(w, "bad sequence", http.StatusBadRequest)
		return
	}
	h.changeMu.Lock()
	current, signal := h.changeSeq, h.changeSignal
	h.changeMu.Unlock()
	if current == since {
		timeout := time.NewTimer(25 * time.Second)
		defer timeout.Stop()
		select {
		case <-signal:
		case <-timeout.C:
		case <-r.Context().Done():
			return
		}
	}
	h.changeMu.Lock()
	current = h.changeSeq
	h.changeMu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(changeResponse{Sequence: current})
}

func (p *Peer) waitForChanges(ctx context.Context, since uint64) (uint64, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := p.request(reqCtx, "GET", "/v3/changes?since="+url.QueryEscape(strconv.FormatUint(since, 10)), nil, nil)
	if err != nil {
		return since, err
	}
	defer resp.Body.Close()
	var body changeResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 128)).Decode(&body); err != nil {
		return since, err
	}
	return body.Sequence, nil
}

func (a *App) localChangeMonitor(ctx context.Context, root string, scanner *Scanner, notify func()) {
	err := watchFolder(ctx, root, func(path string) {
		// Directory renames and changes with preserved timestamps must not
		// reuse old hashes. This is safe even if path names refer to files.
		if path != "" && isInternalPath(path) {
			return
		}
		isDir := path == ""
		if path != "" {
			info, err := os.Stat(filepath.Join(root, filepath.FromSlash(path)))
			isDir = err != nil || info.IsDir() // renamed/deleted dirs need subtree invalidation
		}
		scanner.Invalidate(path, isDir)
		notify()
	})
	if err != nil && ctx.Err() == nil {
		a.addEvent(fmt.Sprintf("Filesystem watcher unavailable: %v; using 10-second fallback", err))
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				notify()
			}
		}
	}
}

func (a *App) remoteChangeMonitor(ctx context.Context, p *Peer) {
	var seen uint64
	for ctx.Err() == nil {
		next, err := p.waitForChanges(ctx, seen)
		if err == nil {
			if next != seen {
				seen = next
				select {
				case a.syncSignal <- struct{}{}:
				default:
				}
			}
			continue
		}
		var status *HTTPStatusError
		if errors.As(err, &status) && status.Status == http.StatusNotFound {
			a.addEvent("Other PC lacks v3 notifications; using periodic sync")
			return
		}
		timer := time.NewTimer(5 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
