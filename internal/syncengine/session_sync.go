package syncengine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"time"

	"folderbridge/internal/fileops"
)

func safetyStop(missing, total int, allow bool) error {
	if !allow && total >= 20 && missing*100 >= total*75 {
		return fmt.Errorf("Safety stop: %d of %d synchronized files would be deleted; approve bulk deletions on this computer if intended", missing, total)
	}
	return nil
}

// observe compares disk only with the last materialized state, never with an
// absent peer or a filtered manifest. Pending writes make response-loss/crash
// recovery distinguish our own completed write from a new local edit.
func (s *sessionRuntime) observe(manifest manifestSnapshot, allow bool) error {
	policy, err := newIgnoreMatcher(manifest.IgnoreFiles)
	if err != nil {
		return err
	}
	names := map[string]bool{}
	for name := range s.state.Files {
		names[name] = true
	}
	for name := range s.state.Applied {
		names[name] = true
	}
	for name := range manifest.Files {
		names[name] = true
	}
	total, missing := 0, 0
	for name := range names {
		a := s.state.Applied[name]
		hash := infoHash(manifest.Files, name)
		if policy.isIgnored(name, false) {
			a.Ignored = true
			s.state.Applied[name] = a
			continue
		}
		if a.Pending != nil && hash == a.Pending.Hash {
			a.Hash = hash
			a.Seen = copyClock(a.Pending.Seen)
			a.Pending = nil
			s.state.Applied[name] = a
		}
		if a.Hash != "" && !a.Ignored {
			total++
			if hash == "" {
				missing++
			}
		}
	}
	// Unambiguous unchanged-content local moves are not mass deletions.
	oldHashes, newHashes := map[string]int{}, map[string]int{}
	for name, a := range s.state.Applied {
		if a.Hash != "" && !a.Ignored && !policy.isIgnored(name, false) && infoHash(manifest.Files, name) == "" {
			oldHashes[a.Hash]++
		}
	}
	for name, info := range manifest.Files {
		if s.state.Applied[name].Hash == "" && !policy.isIgnored(name, false) {
			newHashes[info.Hash]++
		}
	}
	for hash, n := range oldHashes {
		if n == 1 && newHashes[hash] == 1 {
			missing--
		}
	}
	if err := safetyStop(missing, total, allow); err != nil {
		return err
	}
	ordered := make([]string, 0, len(names))
	for name := range names {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)
	for _, name := range ordered {
		if policy.isIgnored(name, false) {
			continue
		}
		a := s.state.Applied[name]
		info := manifest.Files[name]
		if a.Ignored {
			// An ignored absence is never a local deletion. Existing files rejoin as
			// edits based on the last *materialized* clock, not unseen remote history.
			a.Ignored = false
			a.Pending = nil
			if info.Hash == "" {
				a.Hash = ""
				s.state.Applied[name] = a
				continue
			}
			if err := s.state.edit(name, info, a.Seen); err != nil {
				return err
			}
			continue
		}
		if info.Hash != a.Hash || (info.Hash != "" && len(s.state.Files[name]) == 0) {
			if err := s.state.edit(name, info, a.Seen); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *sessionRuntime) snapshot(allow bool) (sessionWire, error) {
	manifest, err := s.scanner.ScanManifest(s.cfg.Folder)
	if err != nil {
		return sessionWire{}, err
	}
	if err = s.observe(manifest, allow); err != nil {
		return sessionWire{}, err
	}
	if err = saveSessionState(s.filename, s.state); err != nil {
		return sessionWire{}, err
	}
	s.signalStateChange()
	policy, err := newIgnoreMatcher(manifest.IgnoreFiles)
	if err != nil {
		return sessionWire{}, err
	}
	files := map[string][]fileVersion{}
	for name, vs := range s.state.Files {
		if !policy.isIgnored(name, false) {
			files[name], err = mergeVersions(nil, vs)
			if err != nil {
				return sessionWire{}, err
			}
		}
	}
	members := map[string]sessionMember{}
	for id, m := range s.state.Members {
		m.Addresses = append([]string{}, m.Addresses...)
		members[id] = m
	}
	return sessionWire{Protocol: 4, Session: s.cfg.Session, Self: s.self, Members: members, Files: files, Contents: manifest.Files}, nil
}

func validateWire(w sessionWire, session, pin string) error {
	if w.Protocol != 4 || w.Session != session || w.Self.ID != pin || validateMember(w.Self) != nil || w.Files == nil || w.Contents == nil || w.Members == nil {
		return errors.New("invalid session snapshot")
	}
	for id, m := range w.Members {
		if id != m.ID || validateMember(m) != nil {
			return errors.New("invalid session roster")
		}
	}
	for name, vs := range w.Files {
		if !validSessionPath(name) || len(vs) == 0 {
			return errors.New("unsafe remote history path")
		}
		if _, err := mergeVersions(nil, vs); err != nil {
			return err
		}
	}
	for name, info := range w.Contents {
		if !validSessionPath(name) || !validHex(info.Hash, 32) || info.Size < 0 {
			return errors.New("invalid remote content manifest")
		}
	}
	return nil
}

func (s *sessionRuntime) merge(w sessionWire, policy *ignoreMatcher) error {
	next := map[string][]fileVersion{}
	for name, vs := range s.state.Files {
		next[name] = vs
	}
	for name, vs := range w.Files {
		if policy.isIgnored(name, false) {
			continue
		}
		for _, v := range vs {
			if v.Clock[s.self.ID] > s.state.Counter {
				return errors.New("remote history contains a future local sequence; local state may have been rolled back")
			}
		}
		merged, err := mergeVersions(next[name], vs)
		if err != nil {
			return err
		}
		next[name] = merged
	}
	if err := s.learnMembers(w.Self, w.Members); err != nil {
		return err
	}
	s.state.Files = next
	if err := saveSessionState(s.filename, s.state); err != nil {
		return err
	}
	s.signalStateChange()
	return nil
}

func versionSource(name string, v fileVersion, contents map[string]fileInfo) string {
	if infoHash(contents, name) == v.File.Hash {
		return name
	}
	conflict := sessionConflictPath(name, v)
	if infoHash(contents, conflict) == v.File.Hash {
		return conflict
	}
	return ""
}

// install stages bytes in the destination directory only. There is no private
// mirror/cache. A source that changed is retried on a subsequent exchange.
func (s *sessionRuntime) install(ctx context.Context, name, target string, v fileVersion, seen versionClock, local map[string]fileInfo, w sessionWire, p *peer) error {
	checker := newPathIgnoreChecker(s.cfg.Folder)
	if ignored, err := checker.isIgnored(target); err != nil {
		return err
	} else if ignored {
		return errors.New("destination became ignored during reconciliation")
	}
	dest, err := fileops.CheckedPath(s.cfg.Folder, target)
	if err != nil {
		return err
	}
	expected := infoHash(local, target)
	if expected == v.File.Hash {
		return nil
	}
	if target != name && expected != "" {
		return errors.New("conflict-copy name already exists with different contents; existing file preserved")
	}
	var body io.ReadCloser
	if source := versionSource(name, v, local); source != "" {
		full, err := fileops.CheckedPath(s.cfg.Folder, source)
		if err != nil {
			return err
		}
		body, err = os.Open(full)
		if err != nil {
			return err
		}
	} else {
		source := versionSource(name, v, w.Contents)
		if source == "" || p == nil {
			return fmt.Errorf("version of %s is not currently available; waiting for its member", name)
		}
		endpoint := "/v4/file?path=" + url.QueryEscape(source) + "&hash=" + v.File.Hash
		var resp *http.Response
		err = retryTransfer(ctx, func() error { var e error; resp, e = s.request(ctx, p, "GET", endpoint, nil); return e })
		if err != nil {
			return err
		}
		body = resp.Body
	}
	defer body.Close()
	a := s.state.Applied[target]
	a.Pending = &pendingFile{Hash: v.File.Hash, Seen: copyClock(seen)}
	s.state.Applied[target] = a
	if err = saveSessionState(s.filename, s.state); err != nil {
		return err
	}
	if err = fileops.StagedWriteGuarded(dest, &io.LimitedReader{R: body, N: v.File.Size}, v.File.Hash, time.Unix(0, v.File.MTime), &expected); err != nil {
		return err
	}
	a.Hash = v.File.Hash
	a.Seen = copyClock(seen)
	a.Pending = nil
	s.state.Applied[target] = a
	local[target] = v.File
	s.scanner.Invalidate(target, false)
	return saveSessionState(s.filename, s.state)
}

func (s *sessionRuntime) apply(ctx context.Context, w sessionWire, p *peer, allow bool, report func(string)) (SyncResult, error) {
	var result SyncResult
	// Re-scan after network I/O so an edit during the exchange is not lost.
	manifest, err := s.scanner.ScanManifest(s.cfg.Folder)
	if err != nil {
		return result, err
	}
	if err = s.observe(manifest, allow); err != nil {
		return result, err
	}
	policy, err := newIgnoreMatcher(manifest.IgnoreFiles)
	if err != nil {
		return result, err
	}
	if err = s.merge(w, policy); err != nil {
		return result, err
	}
	local := manifest.Files
	moves, moveErr := s.applyMoves(ctx, local, policy, report)
	result.Renames = moves.Renames
	if moveErr != nil {
		return result, moveErr
	}
	total, missing := 0, 0
	for name, a := range s.state.Applied {
		if a.Hash != "" && !policy.isIgnored(name, false) {
			total++
			if len(s.state.Files[name]) > 0 && len(liveVersions(s.state.Files[name])) == 0 {
				missing++
			}
		}
	}
	if err = safetyStop(missing, total, allow); err != nil {
		return result, err
	}
	batch, batchErr := s.applyBatches(ctx, w, p, local, policy, report)
	result.Downloaded += batch.Downloaded
	result.BatchDownloads += batch.BatchDownloads
	if batchErr != nil {
		return result, batchErr
	}
	names := make([]string, 0, len(s.state.Files))
	for name := range s.state.Files {
		names = append(names, name)
	}
	// Materialize an original's concurrent versions before processing the
	// independently advertised conflict-copy paths derived from that original.
	parents := map[string]string{}
	for name, vs := range s.state.Files {
		live := liveVersions(vs)
		if len(live) > 1 {
			for _, v := range live[1:] {
				parents[sessionConflictPath(name, v)] = name
			}
		}
	}
	depth := func(name string) int {
		n := 0
		for parents[name] != "" {
			name = parents[name]
			n++
		}
		return n
	}
	sort.Slice(names, func(i, j int) bool {
		di, dj := depth(names[i]), depth(names[j])
		if di != dj {
			return di < dj
		}
		return names[i] < names[j]
	})
	var problems []error
	for _, name := range names {
		if err = ctx.Err(); err != nil {
			return result, err
		}
		ignored, ignoreErr := localIgnoreForPath(s.cfg.Folder, name)
		if ignoreErr != nil {
			problems = append(problems, ignoreErr)
			continue
		}
		if ignored {
			a := s.state.Applied[name]
			a.Ignored = true
			s.state.Applied[name] = a
			continue
		}
		vs := s.state.Files[name]
		live := liveVersions(vs)
		seen := versionsClock(vs)
		result.Examined++
		if len(live) == 0 {
			a := s.state.Applied[name]
			if infoHash(local, name) != "" {
				dest, e := fileops.CheckedPath(s.cfg.Folder, name)
				if e == nil {
					e = fileops.CheckExpected(dest, local[name].Hash)
				}
				if e == nil {
					a.Pending = &pendingFile{Seen: seen}
					s.state.Applied[name] = a
					e = saveSessionState(s.filename, s.state)
				}
				if e == nil {
					e = os.Remove(dest)
				}
				if e != nil {
					problems = append(problems, fmt.Errorf("delete %s: %w", name, e))
					continue
				}
				delete(local, name)
				s.scanner.Invalidate(name, false)
				result.Deleted++
				report("Deleted " + name)
			}
			s.state.Applied[name] = appliedFile{Seen: seen}
			if e := saveSessionState(s.filename, s.state); e != nil {
				return result, e
			}
			continue
		}
		// Preserve all other concurrent live versions before replacing the primary.
		complete := true
		for _, v := range live[1:] {
			conflict := sessionConflictPath(name, v)
			// A conflict copy subsequently edited/deleted by a user is an ordinary
			// independently versioned path; do not recreate or overwrite it forever.
			if history := s.state.Files[conflict]; len(history) > 0 {
				current := infoHash(local, conflict)
				if len(liveVersions(history)) == 0 || (current != "" && current != v.File.Hash) {
					continue
				}
			}
			existed := infoHash(local, conflict) != ""
			if e := s.install(ctx, name, conflict, v, versionClock{}, local, w, p); e != nil {
				problems = append(problems, fmt.Errorf("preserve %s: %w", name, e))
				complete = false
				break
			}
			if !existed {
				result.Conflicts++
				report("Conflict: preserved " + conflict)
			}
		}
		if !complete {
			continue
		}
		v := live[0]
		before := infoHash(local, name)
		if e := s.install(ctx, name, name, v, seen, local, w, p); e != nil {
			problems = append(problems, fmt.Errorf("receive %s: %w", name, e))
			continue
		}
		// Same bytes can acknowledge new equal-content/concurrent clocks without a
		// transfer, but never acknowledge a version whose conflict copy is missing.
		s.state.Applied[name] = appliedFile{Hash: v.File.Hash, Seen: seen}
		if e := saveSessionState(s.filename, s.state); e != nil {
			return result, e
		}
		if before != v.File.Hash {
			result.Downloaded++
			report("Received " + name)
		}
	}
	return result, errors.Join(problems...)
}

func (s *sessionRuntime) exchange(ctx context.Context, m sessionMember, hello sessionHello, allow bool, report func(string)) (SyncResult, error) {
	var result SyncResult
	body, err := json.Marshal(hello)
	if err != nil {
		return result, err
	}
	var lastErr error
	addresses := append([]string{""}, m.Addresses...)
	for _, addr := range addresses {
		if ctx.Err() != nil {
			return result, ctx.Err()
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
		reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		resp, e := s.request(reqCtx, p, "POST", "/v4/exchange", bytes.NewReader(body))
		var w sessionWire
		if e == nil {
			e = decodeSessionJSON(resp.Body, &w)
			resp.Body.Close()
		}
		cancel()
		if e == nil {
			e = validateWire(w, s.cfg.Session, m.ID)
		}
		if e == nil {
			s.mu.Lock()
			result, e = s.apply(ctx, w, p, allow, report)
			s.mu.Unlock()
			if addr != "" {
				p.Close()
			}
			return result, e
		}
		if addr != "" {
			p.Close()
		}
		lastErr = e
	}
	if lastErr == nil {
		lastErr = errors.New("no reachable LAN address; use a current invitation to refresh the member address")
	}
	return result, lastErr
}
