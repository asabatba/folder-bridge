package main

import (
	"sort"
	"strings"
)

type renameAction struct {
	From   string
	To     string
	Hash   string
	Remote bool // true: move on host; false: move on joining PC
}

// detectRenames is deliberately conservative. Both sides must agree on the old
// file's content, only one side may have moved it, and each content hash must
// uniquely identify one old and one new path. Duplicate-content files, two-way
// divergent moves, content modifications and replacements fall back to the
// ordinary conflict-safe reconciliation logic.
func detectRenames(local, remote map[string]FileInfo, base map[string]string) []renameAction {
	type group struct{ old, new []string }
	byHash := map[string]*group{}
	for old, hash := range base {
		if hash == "" {
			continue
		}
		g := byHash[hash]
		if g == nil {
			g = &group{}
			byHash[hash] = g
		}
		// Every baseline path for this content hash counts, including unchanged
		// copies; duplicate-content renames are therefore never guessed.
		g.old = append(g.old, old)
	}
	for name, f := range local {
		if _, was := base[name]; was {
			continue
		}
		g := byHash[f.Hash]
		if g != nil {
			g.new = append(g.new, name)
		}
	}
	// Also include destination paths only on the host.
	for name, f := range remote {
		if _, was := base[name]; was {
			continue
		}
		g := byHash[f.Hash]
		if g != nil {
			g.new = append(g.new, name)
		}
	}
	var actions []renameAction
	for hash, g := range byHash {
		if len(g.old) != 1 || len(g.new) != 1 {
			continue
		}
		old, dst := g.old[0], g.new[0]
		if old == dst || equalFoldPath(old, dst) {
			continue
		}
		lOld, rOld, lNew, rNew := infoHash(local, old), infoHash(remote, old), infoHash(local, dst), infoHash(remote, dst)
		if lOld == "" && rOld == hash && lNew == hash && rNew == "" {
			actions = append(actions, renameAction{From: old, To: dst, Hash: hash, Remote: true})
		} else if rOld == "" && lOld == hash && rNew == hash && lNew == "" {
			actions = append(actions, renameAction{From: old, To: dst, Hash: hash, Remote: false})
		}
	}
	sort.Slice(actions, func(i, j int) bool { return actions[i].From < actions[j].From })
	return actions
}

func equalFoldPath(a, b string) bool { return strings.EqualFold(a, b) }

func supportsFeature(features []string, want string) bool {
	for _, f := range features {
		if f == want {
			return true
		}
	}
	return false
}
