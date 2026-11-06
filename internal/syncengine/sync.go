package syncengine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"folderbridge/internal/fileops"
	"os"
	"path/filepath"
	"strings"
)

type SyncResult struct {
	Uploaded       int `json:"uploaded"`
	Downloaded     int `json:"downloaded"`
	BatchUploads   int `json:"batchUploads"`
	BatchDownloads int `json:"batchDownloads"`
	Deleted        int `json:"deleted"`
	Conflicts      int `json:"conflicts"`
	Renames        int `json:"renames"`
	Examined       int `json:"examined"`
}

// The baseline is a content-hash snapshot of the last successful agreement.
// A deletion only propagates if the file existed in this snapshot.
func syncFolders(ctx context.Context, root string, peer *peer, base map[string]string, scanner *scanner, allowMassDelete bool, report func(string)) (map[string]string, SyncResult, error) {
	var result SyncResult
	localManifest, err := scanner.ScanManifest(root)
	if err != nil {
		return base, result, fmt.Errorf("local scan: %w", err)
	}
	var remoteManifest manifestSnapshot
	err = retryTransfer(ctx, func() error {
		var requestErr error
		remoteManifest, requestErr = peer.manifest(ctx)
		return requestErr
	})
	if err != nil {
		return base, result, fmt.Errorf("remote manifest: %w", err)
	}
	localPolicy, err := newIgnoreMatcher(localManifest.IgnoreFiles)
	if err != nil {
		return base, result, err
	}
	remotePolicy, err := newIgnoreMatcher(remoteManifest.IgnoreFiles)
	if err != nil {
		return base, result, err
	}
	local := localManifest.Files
	remote := remoteManifest.Files
	for name := range remote {
		if _, err := fileops.CheckedPath(root, name); err != nil {
			return base, result, fmt.Errorf("remote supplied unsafe path: %w", err)
		}
	}
	// A path ignored by EITHER PC cannot transfer or cause a deletion.
	// Forget it from the baseline; removing an ignore rule will merge safely.
	ignored := func(name string) bool {
		return localPolicy.isIgnored(name, false) || remotePolicy.isIgnored(name, false)
	}
	for name := range local {
		if ignored(name) {
			delete(local, name)
		}
	}
	for name := range remote {
		if ignored(name) {
			delete(remote, name)
		}
	}
	activeBase := make(map[string]string, len(base))
	for name, hash := range base {
		if !ignored(name) {
			activeBase[name] = hash
		}
	}
	// A move is recognized only if its source still matches the successful
	// baseline, its destination is new and unoccupied, and no hash is ambiguous.
	// Exclude confirmed moves from the mass-delete safety tally.
	var moves []renameAction
	if supportsFeature(remoteManifest.Features, "atomic-rename") {
		moves = detectRenames(local, remote, activeBase)
	}
	movedOld := make(map[string]bool, len(moves))
	for _, m := range moves {
		movedOld[m.From] = true
	}
	// Safety stop: a missing/misconfigured folder must not erase the other PC.
	if !allowMassDelete && len(activeBase) >= 20 {
		localMissing, remoteMissing := 0, 0
		for name, previous := range activeBase {
			if movedOld[name] {
				continue
			}
			l, r := infoHash(local, name), infoHash(remote, name)
			if l == "" && r == previous {
				localMissing++
			}
			if r == "" && l == previous {
				remoteMissing++
			}
		}
		if localMissing >= 15 && localMissing*4 >= len(activeBase)*3 {
			return base, result, fmt.Errorf("Safety stop: %d of %d previously synced files disappeared on this PC. Check the folder, then explicitly confirm bulk deletion if intentional", localMissing, len(activeBase))
		}
		if remoteMissing >= 15 && remoteMissing*4 >= len(activeBase)*3 {
			return base, result, fmt.Errorf("Safety stop: %d of %d previously synced files disappeared on the other PC. Check the folder, then explicitly confirm bulk deletion if intentional", remoteMissing, len(activeBase))
		}
	}
	next := make(map[string]string, len(activeBase))
	for k, v := range activeBase {
		next[k] = v
	}
	// Execute remote/local moves before regular transfers. If a move is
	// refused, stop the cycle rather than attempting a stale delete/upload.
	// The next scan will reconcile changed files using the normal algorithm.
	for _, m := range moves {
		if err := ctx.Err(); err != nil {
			return next, result, err
		}
		var err error
		if m.Remote {
			err = retryTransfer(ctx, func() error { return peer.move(ctx, m.From, m.To, m.Hash) })
		} else {
			err = fileops.MoveUnchanged(root, m.From, m.To, m.Hash)
			if err == nil {
				scanner.Invalidate(m.From, false)
				scanner.Invalidate(m.To, false)
			}
		}
		if err != nil {
			return next, result, fmt.Errorf("safe move %s → %s: %w", m.From, m.To, err)
		}
		if m.Remote {
			remote[m.To] = remote[m.From]
			delete(remote, m.From)
		} else {
			local[m.To] = local[m.From]
			delete(local, m.From)
		}
		delete(next, m.From)
		next[m.To] = m.Hash
		result.Renames++
		report(fmt.Sprintf("Moved without retransferring: %s → %s", m.From, m.To))
	}
	var problems []error
	var uploads, downloads []batchFile
	for _, name := range allKeys(local, remote, activeBase) {
		l, r, b := infoHash(local, name), infoHash(remote, name), activeBase[name]
		if l == r {
			continue
		}
		if l != b && r == b && l != "" && local[name].Size <= maxBatchFileBytes {
			uploads = append(uploads, batchFile{Path: name, Info: local[name], ExpectedDestination: r})
		} else if l == b && r != b && r != "" && remote[name].Size <= maxBatchFileBytes {
			downloads = append(downloads, batchFile{Path: name, Info: remote[name], ExpectedDestination: l})
		}
	}
	completed := make(map[string]bool)
	// Group independent small-file transfers into throughput-tuned concurrent
	// waves. Collect results before touching the sync baseline or event log.
	target, _ := peer.tuner.parameters()
	var batchInterrupted error
	for _, outcome := range peer.runBatches(ctx, root, selectBatchChunksWithTarget(uploads, target), true) {
		if outcome.err == nil {
			result.BatchUploads++
			for _, item := range outcome.chunk {
				completed[item.Path] = true
				result.Uploaded++
				setBaseline(next, item.Path, item.Info.Hash)
			}
			report(fmt.Sprintf("Uploaded %d small files in one batch", len(outcome.chunk)))
		} else if transientTransferError(outcome.err) {
			batchInterrupted = fmt.Errorf("batch upload interrupted; will retry next sync: %w", outcome.err)
		} else {
			report(fmt.Sprintf("Batch upload failed; retrying individually: %v", outcome.err))
		}
	}
	if batchInterrupted != nil {
		return next, result, batchInterrupted
	}
	target, _ = peer.tuner.parameters()
	for _, outcome := range peer.runBatches(ctx, root, selectBatchChunksWithTarget(downloads, target), false) {
		for _, name := range outcome.successes {
			completed[name] = true
			result.Downloaded++
			setBaseline(next, name, remote[name].Hash)
		}
		if len(outcome.successes) > 0 {
			result.BatchDownloads++
			report(fmt.Sprintf("Downloaded %d small files in one batch", len(outcome.successes)))
		}
		if outcome.err != nil {
			if transientTransferError(outcome.err) {
				batchInterrupted = fmt.Errorf("batch download interrupted; will retry next sync: %w", outcome.err)
			} else {
				report(fmt.Sprintf("Batch download incomplete; retrying individually: %v", outcome.err))
			}
		}
	}
	if batchInterrupted != nil {
		return next, result, batchInterrupted
	}

	for _, name := range allKeys(local, remote, activeBase) {
		if err := ctx.Err(); err != nil {
			return next, result, err
		}
		result.Examined++
		if completed[name] {
			continue
		}
		l, r, b := infoHash(local, name), infoHash(remote, name), activeBase[name]
		// Same contents on both sides: no action even if both changed since the last cycle.
		if l == r {
			setBaseline(next, name, l)
			continue
		}
		var actionErr error
		switch {
		case l != b && r == b:
			// Only this PC changed (including local deletion).
			if l != "" {
				actionErr = retryTransfer(ctx, func() error { return peer.put(ctx, root, name, local[name], r) })
				if actionErr == nil {
					result.Uploaded++
					report("Uploaded " + name)
				}
			} else {
				actionErr = retryTransfer(ctx, func() error { return peer.delete(ctx, name, r) })
				if actionErr == nil {
					result.Deleted++
					report("Deleted remote " + name)
				}
			}
		case l == b && r != b:
			// Only the other PC changed (including remote deletion).
			if r != "" {
				actionErr = retryTransfer(ctx, func() error { return peer.get(ctx, root, name, remote[name], l) })
				if actionErr == nil {
					result.Downloaded++
					report("Downloaded " + name)
				}
			} else {
				var filename string
				filename, actionErr = fileops.CheckedPath(root, name)
				if actionErr == nil {
					actionErr = fileops.CheckExpected(filename, l)
				}
				if actionErr == nil {
					actionErr = os.Remove(filename)
				}
				if errors.Is(actionErr, os.ErrNotExist) {
					actionErr = nil
				}
				if actionErr == nil {
					result.Deleted++
					report("Deleted local " + name)
				}
			}
		default:
			// Both changed. A modification wins over a deletion.
			// If both have different contents, preserve remote as an extra file.
			if l == "" {
				actionErr = retryTransfer(ctx, func() error { return peer.get(ctx, root, name, remote[name], l) })
				if actionErr == nil {
					result.Downloaded++
					report("Restored modified file " + name)
				}
			} else if r == "" {
				actionErr = retryTransfer(ctx, func() error { return peer.put(ctx, root, name, local[name], r) })
				if actionErr == nil {
					result.Uploaded++
					report("Restored modified file " + name)
				}
			} else {
				var backup string
				backup, actionErr = conflictName(root, name, local)
				if actionErr == nil {
					actionErr = retryTransfer(ctx, func() error { return peer.getAs(ctx, root, name, backup, remote[name], "") })
				}
				// The backup is saved under a different local name but read from the original remote name.
				if actionErr == nil {
					result.Conflicts++
					report("Conflict: saved copy " + backup)
				}
				if actionErr == nil {
					actionErr = retryTransfer(ctx, func() error { return peer.put(ctx, root, name, local[name], r) })
					if actionErr == nil {
						result.Uploaded++
						report("Uploaded local version " + name)
					}
				}
			}
		}
		if actionErr != nil {
			problems = append(problems, fmt.Errorf("%s: %w", name, actionErr))
			// Don't attempt thousands of requests against a disconnected peer.
			// Completed operations remain in next; the next scan reconciles them.
			if transientTransferError(actionErr) {
				return next, result, errors.Join(problems...)
			}
			continue
		}
		// New contents are identical on both devices after the action.
		if l == b && r != b {
			setBaseline(next, name, r)
		} else if l == "" && r != "" && b != r {
			setBaseline(next, name, r)
		} else {
			setBaseline(next, name, l)
		}
	}
	return next, result, errors.Join(problems...)
}

func setBaseline(next map[string]string, name, hash string) {
	if hash == "" {
		delete(next, name)
	} else {
		next[name] = hash
	}
}

func baselineFilename(home, root, invite string) string {
	key := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(root)) + "\n" + invite))
	return filepath.Join(home, "baseline-"+hex.EncodeToString(key[:12])+".json")
}

func loadBaseline(filename string) (map[string]string, error) {
	data, err := os.ReadFile(filename)
	if errors.Is(err, os.ErrNotExist) {
		return make(map[string]string), nil
	}
	if err != nil {
		return nil, err
	}
	var stored struct {
		Version int               `json:"version"`
		Files   map[string]string `json:"files"`
	}
	if err := json.Unmarshal(data, &stored); err != nil {
		return nil, err
	}
	if stored.Version != 1 || stored.Files == nil {
		return nil, errors.New("unsupported baseline format")
	}
	return stored.Files, nil
}

func saveBaseline(filename string, base map[string]string) error {
	data, err := json.MarshalIndent(struct {
		Version int               `json:"version"`
		Files   map[string]string `json:"files"`
	}{Version: 1, Files: base}, "", "  ")
	if err != nil {
		return err
	}
	return fileops.WriteAtomically(filename, data)
}
