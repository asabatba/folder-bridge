// Package syncengine owns peer operation, reconciliation, watching and durable
// synchronization state. It does not depend on the dashboard, tray or updater.
package syncengine

import (
	"context"
	"errors"
	"fmt"
	"folderbridge/internal/fileops"
	"net/http"
	"path/filepath"
	"sync"
	"time"
)

// Options describes a synchronization session, not its persisted app settings.
// CheckBaseline requests synchronous baseline validation before startup (used
// when the caller must establish initialization health before accepting a build).
type Options struct {
	Mode, Folder, Invite, Secret string
	Enabled, Paused              bool
	CheckBaseline                bool
}

// Status is a detached snapshot: callers never acquire engine/scanner locks.
type Status struct {
	State, Error  string
	LastSync      time.Time
	Result        SyncResult
	Index         ScanStats
	Links, Events []string
	Draining      bool
}

// Engine owns all synchronization contexts, servers, scanners and admission gates.
// Start, Stop and Quiesce serialize lifecycle transitions internally.
type Engine struct {
	mu               sync.RWMutex
	controlMu        sync.Mutex
	syncMu           sync.Mutex
	requestGate      sync.RWMutex
	draining         bool
	syncPersistErr   error
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
	return Status{State: a.state, Error: a.lastError, LastSync: a.lastSync, Result: a.lastResult, Index: stats, Links: links, Events: append([]string{}, a.events...), Draining: a.draining}
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

// Quiesce gates new work and waits for admitted writes and the last client cycle
// to persist its baseline. It never cancels active transfer contexts. A failed
// handoff automatically resumes synchronization; a successful handoff keeps new
// work gated until Stop. The callback must not invoke lifecycle transitions.
func (a *Engine) Quiesce(handoff func() error) error {
	a.controlMu.Lock()
	defer a.controlMu.Unlock()
	a.mu.Lock()
	if a.draining {
		a.mu.Unlock()
		return errors.New("update installation is in progress")
	}
	a.draining = true
	a.mu.Unlock()
	committed := false
	defer func() {
		if !committed {
			a.mu.Lock()
			a.draining = false
			a.mu.Unlock()
			a.RequestSync()
		}
	}()
	a.syncMu.Lock()
	defer a.syncMu.Unlock()
	a.requestGate.Lock()
	defer a.requestGate.Unlock()
	a.mu.RLock()
	err := a.syncPersistErr
	a.mu.RUnlock()
	if err != nil {
		return fmt.Errorf("persist synchronization state before update: %w", err)
	}
	if err := handoff(); err != nil {
		return err
	}
	committed = true
	return nil
}

// hostDuringUpdate drains all admitted handlers, including writes and batches.
func (a *Engine) hostDuringUpdate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.mu.RLock()
		draining := a.draining
		a.mu.RUnlock()
		if draining {
			http.Error(w, "host restarting for approved update", http.StatusServiceUnavailable)
			return
		}
		a.requestGate.RLock()
		defer a.requestGate.RUnlock()
		a.mu.RLock()
		draining = a.draining
		a.mu.RUnlock()
		if draining {
			http.Error(w, "host restarting for approved update", http.StatusServiceUnavailable)
			return
		}
		next.ServeHTTP(w, r)
	})
}
