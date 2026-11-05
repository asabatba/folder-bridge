package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type FileInfo struct {
	Hash  string `json:"hash"`
	Size  int64  `json:"size"`
	MTime int64  `json:"mtime"` // nanoseconds since Unix epoch
}

type cachedHash struct {
	Size  int64
	MTime int64
	Hash  string
}

// Scanner reuses hashes when size and modification time have not changed.
// It never follows symlinks. Mutating files during a scan aborts that scan.
type Scanner struct {
	mu        sync.Mutex
	cache     map[string]cachedHash
	indexPath string
	restored  int
	lastStats ScanStats
}

func NewScanner() *Scanner { return &Scanner{cache: make(map[string]cachedHash)} }

// ScanManifest reads ignore files in each directory before processing its files.
// Ignored directories are pruned; their contents are never hashed or advertised.
func (s *Scanner) ScanManifest(root string) (Manifest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := checkSyncRoot(root); err != nil {
		return Manifest{}, err
	}
	found := make(map[string]FileInfo)
	next := make(map[string]cachedHash)
	var policies []IgnoreFile
	stats := ScanStats{Restored: s.restored}
	var matcher *IgnoreMatcher
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
				matcher = &IgnoreMatcher{}
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
			key.Hash, err = hashStableFile(full, info)
			if err != nil {
				return fmt.Errorf("hash %s: %w", rel, err)
			}
		}
		found[rel] = FileInfo{Hash: key.Hash, Size: key.Size, MTime: key.MTime}
		next[rel] = key
		return nil
	})
	if err != nil {
		return Manifest{}, err
	}
	if err := checkSyncRoot(root); err != nil {
		return Manifest{}, err
	}
	changed := !sameHashIndex(s.cache, next)
	s.cache = next
	stats.Files = len(found)
	s.lastStats = stats
	if changed {
		if err := s.persistLocked(root); err != nil {
			return Manifest{}, err
		}
	}
	return Manifest{Files: found, IgnoreFiles: policies}, nil
}

func (s *Scanner) Scan(root string) (map[string]FileInfo, error) {
	manifest, err := s.ScanManifest(root)
	return manifest.Files, err
}

func hashFile(filename string) (string, error) {
	f, err := os.Open(filename)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func hashStableFile(filename string, before os.FileInfo) (string, error) {
	result, err := hashFile(filename)
	if err != nil {
		return "", err
	}
	after, err := os.Stat(filename)
	if err != nil {
		return "", err
	}
	if after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return "", errors.New("file was modified during scan; retrying on next cycle")
	}
	return result, nil
}

func checkedPath(root, relative string) (string, error) {
	if err := checkSyncRoot(root); err != nil {
		return "", err
	}
	if relative == "" || strings.Contains(relative, "\\") || strings.HasPrefix(relative, "/") || path.Clean(relative) != relative {
		return "", errors.New("invalid relative path")
	}
	native := filepath.FromSlash(relative)
	if !filepath.IsLocal(native) {
		return "", errors.New("path is not local")
	}
	dest := filepath.Join(root, native)
	back, err := filepath.Rel(root, dest)
	if err != nil || back == ".." || strings.HasPrefix(back, ".."+string(os.PathSeparator)) {
		return "", errors.New("path escapes shared directory")
	}
	// Reject symlinks/junctions in the existing path, including parent directories.
	segment := root
	for _, part := range strings.Split(native, string(os.PathSeparator)) {
		segment = filepath.Join(segment, part)
		info, err := os.Lstat(segment)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		if isPathLink(info) {
			return "", errors.New("symbolic links, junctions and reparse points are not allowed")
		}
	}
	return dest, nil
}

func infoHash(m map[string]FileInfo, name string) string {
	if v, ok := m[name]; ok {
		return v.Hash
	}
	return ""
}

func allKeys(a, b map[string]FileInfo, base map[string]string) []string {
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

func conflictName(root, rel string, existing map[string]FileInfo) (string, error) {
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
		filename, err := checkedPath(root, candidate)
		if err != nil {
			return "", err
		}
		if _, err := os.Lstat(filename); errors.Is(err, os.ErrNotExist) {
			return candidate, nil
		}
	}
	return "", errors.New("too many conflict filenames")
}

// A non-nil originalHash prevents overwriting a file that changed after the scan.
// The empty string means that the destination was expected to be absent.
func stagedWriteGuarded(dest string, data io.Reader, expectedHash string, mtime time.Time, originalHash *string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".fb-part-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	h := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(tmp, h), data)
	if copyErr != nil {
		tmp.Close()
		return copyErr
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != expectedHash {
		tmp.Close()
		return fmt.Errorf("checksum mismatch: expected %s, got %s", expectedHash, got)
	}
	// A completed temporary file is flushed before it can become visible.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if !mtime.IsZero() {
		if err := os.Chtimes(tmpName, mtime, mtime); err != nil {
			return err
		}
	}
	return installStagedFile(tmpName, dest, originalHash)
}

func writeJSONAtomically(filename string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(filename), ".fb-part-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err = tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return installStagedFile(tmpName, filename, nil)
}
