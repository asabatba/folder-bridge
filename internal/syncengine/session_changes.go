package syncengine

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Called under the session lock. Only a real history/roster change wakes remote
// members, so equal repeated exchanges do not create a notification feedback loop.
func (s *sessionRuntime) signalStateChange() {
	data, _ := json.Marshal(struct {
		Files   map[string][]fileVersion
		Members map[string]sessionMember
	}{s.state.Files, s.state.Members})
	sum := sha256.Sum256(data)
	if sum != s.signature {
		s.signature = sum
		s.changes.signalChange()
	}
}

// Only the owning reconciliation loop adds workers. Stop first waits for that
// loop, then for workers; no Add races with a zero-count Wait during shutdown.
func (s *sessionRuntime) ensureMonitors() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, m := range s.state.Members {
		if id == s.self.ID {
			continue
		}
		addresses := strings.Join(m.Addresses, "\n")
		if old, ok := s.monitorAddresses[id]; ok && old == addresses {
			continue
		}
		if cancel := s.monitors[id]; cancel != nil {
			cancel()
		}
		ctx, cancel := context.WithCancel(s.ctx)
		s.monitors[id] = cancel
		s.monitorAddresses[id] = addresses
		s.workers.Add(1)
		go func(m sessionMember) { defer s.workers.Done(); s.monitorMember(ctx, m) }(m)
		for i := 0; i < reverseConnections; i++ {
			s.workers.Add(1)
			go func(m sessionMember) { defer s.workers.Done(); s.maintainReverse(ctx, m) }(m)
		}
	}
}

func (s *sessionRuntime) monitorMember(ctx context.Context, m sessionMember) {
	var seen uint64
	for ctx.Err() == nil {
		reachable := false
		addresses := append([]string{""}, m.Addresses...)
		for _, addr := range addresses {
			if ctx.Err() != nil {
				return
			}
			var p *peer
			if addr == "" {
				p = s.reversePeer(m.ID)
				if p == nil {
					continue
				}
			} else {
				p = s.peer(addr, m.ID)
			}
			pollCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			resp, err := s.request(pollCtx, p, "GET", "/v4/changes?since="+url.QueryEscape(strconv.FormatUint(seen, 10)), nil)
			var change changeResponse
			if err == nil {
				err = decodeSessionJSON(resp.Body, &change)
				resp.Body.Close()
			}
			cancel()
			if addr != "" {
				p.Close()
			}
			if err == nil {
				reachable = true
				if change.Sequence != seen {
					seen = change.Sequence
					s.wake()
				}
				break
			}
		}
		if !reachable {
			s.wake()
			timer := time.NewTimer(10 * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}
}
