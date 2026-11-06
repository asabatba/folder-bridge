// Package syncengine owns peer operation, reconciliation, watching and durable
// synchronization state. It does not depend on the dashboard or tray.
package syncengine

import (
	"context"
	"fmt"
	"folderbridge/internal/fileops"
	"net/http"
	"path/filepath"
	"sync"
	"time"
)

// Options describes a synchronization session, not its persisted app settings.
type Options struct {
	Mode, Folder, Invite, Secret string
	Enabled, Paused              bool
}

// Status is a detached snapshot: callers never acquire engine/scanner locks.
type Status struct {
	State, Error  string
	LastSync      time.Time
	Result        SyncResult
	Index         ScanStats
	Links, Events []string
}

// Engine owns synchronization contexts, servers, scanners and persisted state.
// Start and Stop serialize lifecycle transitions internally.
type Engine struct {
	mu               sync.RWMutex
	controlMu        sync.Mutex
	syncMu           sync.Mutex
	home             string
	cfg              Options
	state, lastError string
	lastSync         time.Time
	lastResult       SyncResult
	lastStats        ScanStats
	hostScanner      *scanner
	allowMassDelete  bool
	events           []string
	fingerprint      string
	hostServer       *http.Server
	cancel           context.CancelFunc
	syncSignal       chan struct{}
	notifyFn         func(string, string, bool)
}

// New creates an idle engine. Notifications are delivered outside engine locks.
func New(home string, notify func(string, string, bool)) *Engine {
	return &Engine{home: home, state: "Stopped", syncSignal: make(chan struct{}, 1), notifyFn: notify}
}

// ValidateInvite checks an invitation without exposing peer transport internals.
func ValidateInvite(raw string) error { _, err := parseInvite(raw); return err }

// IgnoreExplanation combines the local and peer policies without changing them.
type IgnoreExplanation struct {
	Local        IgnoreDecision  `json:"local"`
	Remote       *IgnoreDecision `json:"remote,omitempty"`
	Effective    bool            `json:"effective"`
	RemoteStatus string          `json:"remoteStatus"`
}

// ExplainIgnore inspects local rules and, in join mode, the peer's rules.
// Remote unavailability is reported in the result, not as a local error.
func ExplainIgnore(ctx context.Context, cfg Options, rel string) (IgnoreExplanation, error) {
	root, invite := cfg.Folder, cfg.Invite
	if _, err := fileops.CheckedPath(root, rel); err != nil {
		return IgnoreExplanation{}, err
	}
	checker := newPathIgnoreChecker(root)
	dir := filepath.ToSlash(filepath.Dir(rel))
	if dir == "." {
		dir = ""
	}
	matcher, err := checker.matcher(dir)
	if err != nil {
		return IgnoreExplanation{}, fmt.Errorf("local ignore policy: %w", err)
	}
	local := matcher.Explain(rel, false)
	reply := IgnoreExplanation{Local: local, Effective: local.Ignored, RemoteStatus: "Unavailable in Host mode; inspect on the joining PC for both policies"}
	if cfg.Mode == "join" {
		reply.RemoteStatus = "Unavailable"
		if parsed, err := parseInvite(invite); err == nil {
			p := newPeer(parsed)
			reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			manifest, err := p.manifest(reqCtx)
			cancel()
			p.Close()
			if err != nil {
				reply.RemoteStatus = "Could not read peer rules: " + err.Error()
			} else if m, err := newIgnoreMatcher(manifest.IgnoreFiles); err == nil {
				remote := m.Explain(rel, false)
				reply.Remote = &remote
				reply.Effective = local.Ignored || remote.Ignored
				reply.RemoteStatus = "Available"
			} else {
				reply.RemoteStatus = "Invalid remote ignore policy: " + err.Error()
			}
		}
	}
	return reply, nil
}

// Snapshot returns synchronization status and pairing links without live internals.
func (a *Engine) Snapshot() Status {
	a.mu.RLock()
	defer a.mu.RUnlock()
	links := []string{}
	if a.cfg.Mode == "host" && a.cfg.Enabled && a.fingerprint != "" {
		for _, addr := range lanAddresses() {
			links = append(links, formatInvite(addr, a.cfg.Secret, a.fingerprint))
		}
	}
	stats := a.lastStats
	if a.hostScanner != nil {
		stats = a.hostScanner.Stats()
	}
	return Status{State: a.state, Error: a.lastError, LastSync: a.lastSync, Result: a.lastResult, Index: stats, Links: links, Events: append([]string{}, a.events...)}
}

// ReportError records a failed synchronization control operation. Repeated
// identical errors do not emit repeated notifications.
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

// RecordEvent adds a control action to the same bounded history as transfers.
func (a *Engine) RecordEvent(msg string) { a.addEvent(msg) }

// ApproveBulkDelete authorizes one cycle, then requests reconciliation.
func (a *Engine) ApproveBulkDelete() {
	a.mu.Lock()
	a.allowMassDelete = true
	a.mu.Unlock()
	a.RequestSync()
}
