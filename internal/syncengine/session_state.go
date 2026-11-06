package syncengine

// The v4 session is a collection of multi-value registers, one per relative
// path. Clocks describe observed edits, not wall-clock time or peer availability.
// Tombstones are retained: an offline member is never presumed to have left.
import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"folderbridge/internal/fileops"
)

type versionClock map[string]uint64

type fileVersion struct {
	Device   string       `json:"device"`
	Sequence uint64       `json:"sequence"`
	Clock    versionClock `json:"clock"`
	File     fileInfo     `json:"file"` // empty hash is an explicit deletion
}

type appliedFile struct {
	Hash    string       `json:"hash"`
	Seen    versionClock `json:"seen"`
	Ignored bool         `json:"ignored,omitempty"`
	Pending *pendingFile `json:"pending,omitempty"`
}

type pendingFile struct {
	Hash string       `json:"hash"`
	Seen versionClock `json:"seen"`
}

type sessionMember struct {
	ID        string   `json:"id"` // SHA-256 certificate fingerprint
	Name      string   `json:"name"`
	Addresses []string `json:"addresses"`
	Revision  uint64   `json:"revision"` // durable incarnation; stale gossip cannot restore old addresses
}

type sessionState struct {
	Version int                      `json:"version"`
	Session string                   `json:"session"`
	Device  string                   `json:"device"`
	Root    string                   `json:"root"`
	Counter uint64                   `json:"counter"`
	Files   map[string][]fileVersion `json:"files"`
	Applied map[string]appliedFile   `json:"applied"`
	Members map[string]sessionMember `json:"members"`
}

func validHex(s string, bytes int) bool {
	if len(s) != bytes*2 || s != strings.ToLower(s) {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func validSessionPath(name string) bool {
	return name != "" && name != "." && !strings.Contains(name, "\\") && !strings.HasPrefix(name, "/") && path.Clean(name) == name && filepath.IsLocal(filepath.FromSlash(name)) && !isInternalPath(name)
}

func copyClock(c versionClock) versionClock {
	out := versionClock{}
	for id, n := range c {
		out[id] = n
	}
	return out
}

func includeClock(dst, src versionClock) {
	for id, n := range src {
		if dst[id] < n {
			dst[id] = n
		}
	}
}

func clockIncludes(a, b versionClock) bool {
	for id, n := range b {
		if a[id] < n {
			return false
		}
	}
	return true
}

func versionsClock(vs []fileVersion) versionClock {
	out := versionClock{}
	for _, v := range vs {
		includeClock(out, v.Clock)
	}
	return out
}

func validateVersion(v fileVersion) error {
	if !validHex(v.Device, 32) || v.Sequence == 0 || v.Clock[v.Device] != v.Sequence {
		return errors.New("invalid version identity")
	}
	for id, n := range v.Clock {
		if !validHex(id, 32) || n == 0 {
			return errors.New("invalid version clock")
		}
	}
	if v.File.Hash != "" && !validHex(v.File.Hash, 32) {
		return errors.New("invalid content hash")
	}
	if v.File.Size < 0 || (v.File.Hash == "" && (v.File.Size != 0 || v.File.MTime != 0)) {
		return errors.New("invalid content metadata")
	}
	return nil
}

// mergeVersions is deterministic, associative, commutative and idempotent.
// Even concurrent equal-content versions retain both clocks; equality of bytes
// is not evidence that one edit observed another.
func mergeVersions(a, b []fileVersion) ([]fileVersion, error) {
	all := append(append([]fileVersion{}, a...), b...)
	byID := map[string]fileVersion{}
	for _, v := range all {
		if err := validateVersion(v); err != nil {
			return nil, err
		}
		key := fmt.Sprintf("%s:%d", v.Device, v.Sequence)
		if old, ok := byID[key]; ok {
			if !clockIncludes(old.Clock, v.Clock) || !clockIncludes(v.Clock, old.Clock) || old.File != v.File {
				return nil, errors.New("version identity reused with different contents")
			}
		} else {
			v.Clock = copyClock(v.Clock)
			byID[key] = v
		}
	}
	out := []fileVersion{}
	for key, v := range byID {
		dominated := false
		for other, w := range byID {
			if key != other && clockIncludes(w.Clock, v.Clock) {
				if clockIncludes(v.Clock, w.Clock) {
					return nil, errors.New("distinct edits have identical clocks")
				}
				dominated = true
				break
			}
		}
		if !dominated {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Device != out[j].Device {
			return out[i].Device < out[j].Device
		}
		return out[i].Sequence < out[j].Sequence
	})
	return out, nil
}

// A live concurrent edit wins over a deletion. Equal bytes need only one
// physical copy. Ordering is stable on every member, not based on scan order.
func liveVersions(vs []fileVersion) []fileVersion {
	out := []fileVersion{}
	seen := map[string]bool{}
	for _, v := range vs {
		if v.File.Hash != "" && !seen[v.File.Hash] {
			out = append(out, v)
			seen[v.File.Hash] = true
		}
	}
	return out
}

func sessionConflictPath(name string, v fileVersion) string {
	ext := path.Ext(name)
	return fmt.Sprintf("%s.conflict-%s-%d%s", strings.TrimSuffix(name, ext), v.Device, v.Sequence, ext)
}

func (s *sessionState) edit(name string, info fileInfo, seen versionClock) error {
	if !validSessionPath(name) {
		return errors.New("invalid session path")
	}
	if s.Counter == math.MaxUint64 {
		return errors.New("device sequence exhausted; fresh pairing required")
	}
	s.Counter++
	clock := copyClock(seen)
	clock[s.Device] = s.Counter
	v := fileVersion{Device: s.Device, Sequence: s.Counter, Clock: clock, File: info}
	merged, err := mergeVersions(s.Files[name], []fileVersion{v})
	if err != nil {
		return err
	}
	s.Files[name] = merged
	s.Applied[name] = appliedFile{Hash: info.Hash, Seen: clock}
	return nil
}

func (s *sessionState) validate() error {
	if s.Version != 4 || !validHex(s.Session, 16) || !validHex(s.Device, 32) || s.Root == "" || s.Files == nil || s.Applied == nil || s.Members == nil {
		return errors.New("invalid session state header")
	}
	for name, vs := range s.Files {
		if !validSessionPath(name) || len(vs) == 0 {
			return errors.New("invalid session file history")
		}
		canonical, err := mergeVersions(nil, vs)
		if err != nil || len(canonical) != len(vs) {
			return errors.New("invalid session file versions")
		}
		for _, v := range vs {
			if v.Clock[s.Device] > s.Counter {
				return errors.New("session sequence moved backwards")
			}
		}
	}
	for name, a := range s.Applied {
		if !validSessionPath(name) || (a.Hash != "" && !validHex(a.Hash, 32)) {
			return errors.New("invalid applied file")
		}
		for id, n := range a.Seen {
			if !validHex(id, 32) || n == 0 || (id == s.Device && n > s.Counter) {
				return errors.New("invalid applied clock")
			}
		}
		if a.Pending != nil {
			if a.Pending.Hash != "" && !validHex(a.Pending.Hash, 32) {
				return errors.New("invalid pending hash")
			}
			for id, n := range a.Pending.Seen {
				if !validHex(id, 32) || n == 0 || (id == s.Device && n > s.Counter) {
					return errors.New("invalid pending clock")
				}
			}
		}
	}
	for id, m := range s.Members {
		if id != m.ID || validateMember(m) != nil {
			return errors.New("invalid saved member")
		}
	}
	return nil
}

func loadSessionState(filename, session, device, root string) (*sessionState, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	var s sessionState
	if err = json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("read session state: %w", err)
	}
	if err = s.validate(); err != nil {
		return nil, err
	}
	if s.Session != session || s.Device != device || s.Root != root {
		return nil, errors.New("session state belongs to a different folder or device; fresh pairing required")
	}
	return &s, nil
}

func saveSessionState(filename string, s *sessionState) error {
	if err := s.validate(); err != nil {
		return err
	}
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return fileops.WriteAtomically(filename, data)
}
