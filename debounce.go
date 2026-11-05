package main

import (
	"context"
	"time"
)

// Coalesce noisy editor save operations and Windows rename notifications into
// one scan, bounded so a continuously changing directory still makes progress.
func debounceChanges(ctx context.Context, signal <-chan struct{}, quiet, maximum time.Duration) bool {
	quietTimer := time.NewTimer(quiet)
	defer quietTimer.Stop()
	deadline := time.NewTimer(maximum)
	defer deadline.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-quietTimer.C:
			return true
		case <-deadline.C:
			return true
		case <-signal:
			if !quietTimer.Stop() {
				select {
				case <-quietTimer.C:
				default:
				}
			}
			quietTimer.Reset(quiet)
		}
	}
}
