package syncengine

import (
	"errors"
	"fmt"
	"folderbridge/internal/fileops"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type fileInfo struct {
	Hash  string `json:"hash"`
	Size  int64  `json:"size"`
	MTime int64  `json:"mtime"` // nanoseconds since Unix epoch
}

type cachedHash struct {
	Size  int64
	MTime int64
	Hash  string
}

// scanner reuses hashes when size and modification time have not changed.
// It never follows symlinks. Mutating files during a scan aborts that scan.
type scanner struct {
	mu        sync.Mutex
	cache     map[string]cachedHash
	indexPath string
	restored  int
	lastStats ScanStats
}

func newScanner() *scanner { return &scanner{cache: make(map[string]cachedHash)} }

// ScanManifest reads ignore files in each directory before processing its files.
// Ignored directories are pruned; their contents are never hashed or advertised.
func (s *scanner) ScanManifest(root string) (manifestSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := fileops.CheckSyncRoot(root); err != nil {
		return manifestSnapshot{}, err
	}
	found := make(map[string]fileInfo)
	next := make(map[string]cachedHash)
	var policies []ignoreFile
	stats := ScanStats{Restored: s.restored}
	var matcher *ignoreMatcher
	err := filepath.WalkDir(root, func(full string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if full == root && (!d.IsDir() || d.Type()&os.ModeSymlink != 0) {
			return errors.New("sync root changed during scan")
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		rel, err := filepath.Rel(root, full)
		if err != nil {
			return err
		}
		if rel == "." {
			rel = ""
		} else {
			rel = filepath.ToSlash(rel)
		}
		if rel != "" && matcher.isIgnored(rel, d.IsDir()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			chunk, err := readIgnoreFilesInDir(root, rel)
			if err != nil {
				return err
			}
			policies = append(policies, chunk...)
			if matcher == nil {
				matcher = &ignoreMatcher{}
			}
			return matcher.appendFiles(chunk)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		key := cachedHash{Size: info.Size(), MTime: info.ModTime().UnixNano()}
		if old, ok := s.cache[rel]; ok && key.Size == old.Size && key.MTime == old.MTime {
			key.Hash = old.Hash
			stats.Reused++
		} else {
			stats.Hashed++
			key.Hash, err = fileops.HashStableFile(full, info)
			if err != nil {
				return fmt.Errorf("hash %s: %w", rel, err)
			}
		}
		found[rel] = fileInfo{Hash: key.Hash, Size: key.Size, MTime: key.MTime}
		next[rel] = key
		return nil
	})
	if err != nil {
		return manifestSnapshot{}, err
	}
	if err := fileops.CheckSyncRoot(root); err != nil {
		return manifestSnapshot{}, err
	}
	changed := !sameHashIndex(s.cache, next)
	s.cache = next
	stats.Files = len(found)
	s.lastStats = stats
	if changed {
		if err := s.persistLocked(root); err != nil {
			return manifestSnapshot{}, err
		}
	}
	return manifestSnapshot{Files: found, IgnoreFiles: policies}, nil
}
func (s *scanner) Scan(root string) (map[string]fileInfo, error) {
	manifest, err := s.ScanManifest(root)
	return manifest.Files, err
}

func infoHash(m map[string]fileInfo, name string) string {
	if v, ok := m[name]; ok {
		return v.Hash
	}
	return ""
}
func allKeys(a, b map[string]fileInfo, base map[string]string) []string {
	keys := make(map[string]bool)
	for k := range a {
		keys[k] = true
	}
	for k := range b {
		keys[k] = true
	}
	for k := range base {
		keys[k] = true
	}
	out := make([]string, 0, len(keys))
	for k := range keys {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
func conflictName(root, rel string, existing map[string]fileInfo) (string, error) {
	ext := path.Ext(rel)
	base := strings.TrimSuffix(rel, ext)
	stamp := time.Now().UTC().Format("20060102-150405")
	for i := 0; i < 1000; i++ {
		additional := ""
		if i > 0 {
			additional = fmt.Sprintf("-%d", i)
		}
		candidate := fmt.Sprintf("%s.conflict-from-other-PC-%s%s%s", base, stamp, additional, ext)
		if _, present := existing[candidate]; present {
			continue
		}
		filename, err := fileops.CheckedPath(root, candidate)
		if err != nil {
			return "", err
		}
		if _, err := os.Lstat(filename); errors.Is(err, os.ErrNotExist) {
			return candidate, nil
		}
	}
	return "", errors.New("too many conflict filenames")
}
