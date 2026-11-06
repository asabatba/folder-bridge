package syncengine

import (
	"context"
	"fmt"

	"folderbridge/internal/fileops"
)

// Reuse the existing bounded compressed-batch scheduler for ordinary live
// versions. Persist every write intent before workers start; collect bookkeeping
// only after joining them. A partially received archive is safe to retry.
func (s *sessionRuntime) applyBatches(ctx context.Context, w sessionWire, p *peer, local map[string]fileInfo, policy *ignoreMatcher, report func(string)) (SyncResult, error) {
	var result SyncResult
	if p == nil {
		return result, nil
	}
	candidates := []batchFile{}
	for name, vs := range s.state.Files {
		live := liveVersions(vs)
		if policy.isIgnored(name, false) || len(live) != 1 {
			continue
		}
		v := live[0]
		if infoHash(local, name) == v.File.Hash || infoHash(w.Contents, name) != v.File.Hash {
			continue
		}
		candidates = append(candidates, batchFile{Path: name, Info: v.File, ExpectedDestination: infoHash(local, name)})
	}
	target, _ := p.tuner.parameters()
	chunks := selectBatchChunksWithTarget(candidates, target)
	for _, chunk := range chunks {
		for _, item := range chunk {
			a := s.state.Applied[item.Path]
			a.Pending = &pendingFile{Hash: item.Info.Hash, Seen: versionsClock(s.state.Files[item.Path])}
			s.state.Applied[item.Path] = a
		}
	}
	if len(chunks) == 0 {
		return result, nil
	}
	if err := saveSessionState(s.filename, s.state); err != nil {
		return result, err
	}
	outcomes := p.runBatches(ctx, s.cfg.Folder, chunks, false)
	for _, outcome := range outcomes {
		if len(outcome.successes) > 0 {
			result.BatchDownloads++
		}
		for _, name := range outcome.successes {
			info := w.Contents[name]
			a := s.state.Applied[name]
			a.Hash = info.Hash
			a.Seen = versionsClock(s.state.Files[name])
			a.Pending = nil
			s.state.Applied[name] = a
			local[name] = info
			s.scanner.Invalidate(name, false)
			result.Downloaded++
		}
		if outcome.err != nil {
			report("Batch incomplete; retrying remaining files individually: " + outcome.err.Error())
		}
	}
	if result.Downloaded > 0 {
		report(fmt.Sprintf("Received %d files in %d compressed batch(es)", result.Downloaded, result.BatchDownloads))
	}
	return result, saveSessionState(s.filename, s.state)
}

// A remote rename is expressed as an observed tombstone plus a live new path.
// Only unambiguous equal-content moves are optimized; no metadata guesses or
// overwrites are needed, and edits concurrent with the move retain normal safety.
func (s *sessionRuntime) applyMoves(ctx context.Context, local map[string]fileInfo, policy *ignoreMatcher, report func(string)) (SyncResult, error) {
	var result SyncResult
	sources, targets := map[string][]string{}, map[string][]string{}
	for name, vs := range s.state.Files {
		if policy.isIgnored(name, false) {
			continue
		}
		live := liveVersions(vs)
		if len(live) == 0 && infoHash(local, name) != "" {
			sources[local[name].Hash] = append(sources[local[name].Hash], name)
		}
		if len(live) == 1 && infoHash(local, name) == "" {
			targets[live[0].File.Hash] = append(targets[live[0].File.Hash], name)
		}
	}
	for hash, froms := range sources {
		tos := targets[hash]
		if len(froms) != 1 || len(tos) != 1 {
			continue
		}
		if err := ctx.Err(); err != nil {
			return result, err
		}
		from, to := froms[0], tos[0]
		ignored, err := localIgnoreForPath(s.cfg.Folder, from)
		if err != nil {
			return result, err
		}
		if ignored {
			continue
		}
		ignored, err = localIgnoreForPath(s.cfg.Folder, to)
		if err != nil {
			return result, err
		}
		if ignored {
			continue
		}
		sourceApplied, targetApplied := s.state.Applied[from], s.state.Applied[to]
		sourceApplied.Pending = &pendingFile{Seen: versionsClock(s.state.Files[from])}
		targetApplied.Pending = &pendingFile{Hash: hash, Seen: versionsClock(s.state.Files[to])}
		s.state.Applied[from] = sourceApplied
		s.state.Applied[to] = targetApplied
		if err := saveSessionState(s.filename, s.state); err != nil {
			return result, err
		}
		if err := fileops.MoveUnchanged(s.cfg.Folder, from, to, hash); err != nil {
			continue
		} // ordinary guarded transfers reconcile next
		s.state.Applied[from] = appliedFile{Seen: sourceApplied.Pending.Seen}
		s.state.Applied[to] = appliedFile{Hash: hash, Seen: targetApplied.Pending.Seen}
		local[to] = local[from]
		delete(local, from)
		s.scanner.Invalidate(from, false)
		s.scanner.Invalidate(to, false)
		if err := saveSessionState(s.filename, s.state); err != nil {
			return result, err
		}
		result.Renames++
		report("Moved without transfer: " + from + " → " + to)
	}
	return result, nil
}
