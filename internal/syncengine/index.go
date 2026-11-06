package syncengine

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"folderbridge/internal/fileops"
	"os"
	"path/filepath"
	"strings"
)

// The persistent index only accelerates hashing. It is never authoritative for
// existence, deletion, ignore rules, or conflict resolution: those always use
// the live filesystem. Invalid or damaged indices are discarded safely.
const indexVersion = 1

type persistedIndex struct {
	Version int                   `json:"version"`
	Root    string                `json:"root"`
	Entries map[string]cachedHash `json:"entries"`
}

func indexFilename(home, root string) string {
	key := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(root))))
	return filepath.Join(home, "index-"+hex.EncodeToString(key[:12])+".json")
}

func newPersistentScanner(root, home string) *scanner {
	s := newScanner()
	s.indexPath = indexFilename(home, root)
	data, err := os.ReadFile(s.indexPath)
	if err != nil || len(data) > 64<<20 {
		return s
	}
	var record persistedIndex
	if err := json.Unmarshal(data, &record); err != nil || record.Version != indexVersion || record.Root != filepath.Clean(root) {
		return s
	}
	for name, entry := range record.Entries {
		if name == "" || strings.Contains(name, "\\") || filepath.ToSlash(filepath.Clean(name)) != name || !validDigest(entry.Hash, false) || entry.Size < 0 {
			return s
		}
	}
	if record.Entries != nil {
		s.cache = record.Entries
		s.restored = len(record.Entries)
	}
	return s
}

func sameHashIndex(a, b map[string]cachedHash) bool {
	if len(a) != len(b) {
		return false
	}
	for name, x := range a {
		if y, ok := b[name]; !ok || y != x {
			return false
		}
	}
	return true
}

func (s *scanner) persistLocked(root string) error {
	if s.indexPath == "" {
		return nil
	}
	data, err := json.Marshal(persistedIndex{Version: indexVersion, Root: filepath.Clean(root), Entries: s.cache})
	if err != nil {
		return err
	}
	if err := fileops.WriteAtomically(s.indexPath, data); err != nil {
		return fmt.Errorf("save hash index: %w", err)
	}
	return nil
}

// Invalidate causes a changed file to be rehashed even if an application kept
// the same modification time and size. A directory event invalidates its subtree.
func (s *scanner) Invalidate(relative string, directory bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	relative = filepath.ToSlash(filepath.Clean(relative))
	if relative == "." || relative == "" {
		clear(s.cache)
		return
	}
	delete(s.cache, relative)
	if directory {
		for name := range s.cache {
			if strings.HasPrefix(name, relative+"/") {
				delete(s.cache, name)
			}
		}
	}
}

func (s *scanner) Stats() ScanStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastStats
}

// Counts describe the latest directory walk, not network transfer activity.
type ScanStats struct {
	Files    int `json:"files"`
	Hashed   int `json:"hashed"`
	Reused   int `json:"reused"`
	Restored int `json:"restored"`
}
