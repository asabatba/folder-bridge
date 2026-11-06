// Package syncengine owns peer sessions, reconciliation, watching and durable
// synchronization state. It does not depend on the dashboard or tray.
package syncengine

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"folderbridge/internal/fileops"
)

// Options describes one explicitly prepared equal-peer session.
type Options struct {
	Mode, Folder, Invite, Secret string
	Session, Name                string
	// ListenAddress defaults to the LAN port; callers can isolate instances/tests.
	ListenAddress   string
	Enabled, Paused bool
}

type Status struct {
	State, Error  string
	Session       string
	Members       []MemberStatus
	LastSync      time.Time
	Result        SyncResult
	Index         ScanStats
	Links, Events []string
}

type Engine struct {
	mu               sync.RWMutex
	controlMu        sync.Mutex
	home             string
	cfg              Options
	state, lastError string
	lastSync         time.Time
	lastResult       SyncResult
	lastStats        ScanStats
	hostScanner      *scanner
	allowMassDelete  bool
	events           []string
	session          *sessionRuntime
	syncSignal       chan struct{}
	notifyFn         func(string, string, bool)
}

func New(home string, notify func(string, string, bool)) *Engine {
	return &Engine{home: home, state: "Stopped", syncSignal: make(chan struct{}, 1), notifyFn: notify}
}

func ValidateInvite(raw string) error { _, err := parseSessionInvite(raw); return err }

type IgnoreExplanation struct {
	Local        IgnoreDecision  `json:"local"`
	Remote       *IgnoreDecision `json:"remote,omitempty"`
	Effective    bool            `json:"effective"`
	RemoteStatus string          `json:"remoteStatus"`
}

// ExplainIgnore evaluates this device's policy only. Another member's exclusions
// never prohibit sharing between the other members of a session.
func ExplainIgnore(ctx context.Context, cfg Options, rel string) (IgnoreExplanation, error) {
	if err := ctx.Err(); err != nil {
		return IgnoreExplanation{}, err
	}
	if _, err := fileops.CheckedPath(cfg.Folder, rel); err != nil {
		return IgnoreExplanation{}, err
	}
	dir := filepath.ToSlash(filepath.Dir(rel))
	if dir == "." {
		dir = ""
	}
	matcher, err := newPathIgnoreChecker(cfg.Folder).matcher(dir)
	if err != nil {
		return IgnoreExplanation{}, fmt.Errorf("local ignore policy: %w", err)
	}
	local := matcher.Explain(rel, false)
	return IgnoreExplanation{Local: local, Effective: local.Ignored, RemoteStatus: "Device-local policy; other members apply their own rules"}, nil
}

// Snapshot reads detached status only; a long scan or transfer cannot block UI.
func (a *Engine) Snapshot() Status {
	a.mu.RLock()
	out := Status{State: a.state, Error: a.lastError, Session: a.cfg.Session, LastSync: a.lastSync, Result: a.lastResult, Index: a.lastStats, Links: []string{}, Members: []MemberStatus{}, Events: append([]string{}, a.events...)}
	s := a.session
	a.mu.RUnlock()
	if s != nil {
		for _, addr := range s.self.Addresses {
			out.Links = append(out.Links, formatSessionInvite(SessionCredentials{ID: s.cfg.Session, Secret: s.cfg.Secret}, addr, s.self.ID))
		}
		s.viewMu.RLock()
		out.Members = append(out.Members, s.memberView...)
		s.viewMu.RUnlock()
		sort.Slice(out.Members, func(i, j int) bool {
			if out.Members[i].Local != out.Members[j].Local {
				return out.Members[i].Local
			}
			return out.Members[i].ID < out.Members[j].ID
		})
	}
	return out
}

func (a *Engine) ReportError(msg string) {
	a.mu.Lock()
	changed := a.lastError != msg
	a.lastError = msg
	a.state = "Error — will retry"
	a.mu.Unlock()
	if changed {
		a.notify("FolderBridge error", msg, true)
	}
}
func (a *Engine) notify(title, msg string, warning bool) {
	if a.notifyFn != nil {
		a.notifyFn(title, msg, warning)
	}
}
func (a *Engine) RecordEvent(msg string) { a.addEvent(msg) }
func (a *Engine) ApproveBulkDelete() {
	a.mu.Lock()
	a.allowMassDelete = true
	a.mu.Unlock()
	a.RequestSync()
}
