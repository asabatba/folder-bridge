package syncengine

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Start resumes an explicitly prepared v4 session. Legacy roles never open a
// listener or migrate their pairwise baseline implicitly.
func (a *Engine) Start(cfg Options) error {
	a.controlMu.Lock()
	defer a.controlMu.Unlock()
	a.mu.Lock()
	if a.session != nil {
		a.mu.Unlock()
		return errors.New("stop the active session before starting another")
	}
	a.cfg = cfg
	a.lastError = ""
	if !cfg.Enabled || cfg.Mode == "off" || cfg.Mode == "" || cfg.Paused {
		a.state = "Stopped"
		if cfg.Paused && cfg.Enabled && cfg.Mode != "off" {
			a.state = "Paused"
		}
		a.mu.Unlock()
		return nil
	}
	if cfg.Mode != "session" {
		a.state = "Fresh pairing required"
		a.mu.Unlock()
		return errors.New("old host/guest settings are preserved; create or join a new sharing session")
	}
	a.mu.Unlock()
	address := cfg.ListenAddress
	if address == "" {
		address = ":" + peerPort
	}
	s, err := newSessionRuntime(a.home, cfg, address, a.RequestSync)
	if err != nil {
		return err
	}
	if cleaned, e := cleanupStaleParts(cfg.Folder, time.Now()); e != nil {
		a.addEvent("Partial-file cleanup skipped: " + e.Error())
	} else if cleaned > 0 {
		a.addEvent(fmt.Sprintf("Removed %d stale partial files", cleaned))
	}
	a.mu.Lock()
	a.session = s
	a.hostScanner = s.scanner
	a.state = "Connecting to session members"
	a.mu.Unlock()
	s.workers.Add(1)
	go func() {
		defer s.workers.Done()
		a.localChangeMonitor(s.ctx, cfg.Folder, s.scanner, func() { s.changes.signalChange(); a.RequestSync() })
	}()
	go a.sessionLoop(s)
	return nil
}

// Stop waits for the old reconciliation loop before another session may start.
// Canceled transfers cannot publish a new baseline into a replacement session.
func (a *Engine) Stop() {
	a.controlMu.Lock()
	defer a.controlMu.Unlock()
	a.mu.Lock()
	s := a.session
	a.session = nil
	a.hostScanner = nil
	a.state = "Stopped"
	a.mu.Unlock()
	if s != nil {
		s.gate.Lock()
		s.closing = true
		s.gate.Unlock()
		s.cancel()
		s.closeReverse()
		_ = s.server.Close()
		<-s.done
		s.workers.Wait()
		s.handlers.Wait()
	}
	a.mu.Lock()
	a.state = "Stopped"
	a.mu.Unlock()
}

func (a *Engine) RequestSync() {
	select {
	case a.syncSignal <- struct{}{}:
	default:
	}
}

func (a *Engine) addEvent(msg string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append([]string{time.Now().Format("15:04:05") + "  " + msg}, a.events...)
	if len(a.events) > 20 {
		a.events = a.events[:20]
	}
}

func (a *Engine) sessionLoop(s *sessionRuntime) {
	defer close(s.done)
	// Per-member long polls and native local events trigger reconciliation. A
	// full safety scan remains independent of notifications.
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	a.runSessionCycle(s)
	s.ensureMonitors()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
		case <-a.syncSignal:
			if !debounceChanges(s.ctx, a.syncSignal, 350*time.Millisecond, 2*time.Second) {
				return
			}
		}
		a.runSessionCycle(s)
		s.ensureMonitors()
	}
}

func (a *Engine) runSessionCycle(s *sessionRuntime) {
	if s.ctx.Err() != nil {
		return
	}
	a.mu.Lock()
	allow := a.allowMassDelete
	a.allowMassDelete = false
	a.state = "Synchronizing session"
	a.mu.Unlock()
	s.mu.Lock()
	wire, err := s.snapshot(allow)
	s.mu.Unlock()
	if err != nil {
		if s.ctx.Err() == nil {
			a.ReportError(err.Error())
		}
		return
	}
	ids := make([]string, 0, len(wire.Members))
	for id := range wire.Members {
		if id != s.self.ID {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	result := SyncResult{}
	var problems []error
	offline := 0
	for _, id := range ids {
		if s.ctx.Err() != nil {
			return
		}
		s.mu.Lock()
		next := s.nextTry[id]
		previousMemberError := s.memberStatus[id].Error
		s.mu.Unlock()
		if !allow && time.Now().Before(next) && s.reversePeer(id) == nil {
			if strings.Contains(previousMemberError, "Safety stop:") {
				problems = append(problems, errors.New(previousMemberError))
			}
			offline++
			continue
		}
		m := wire.Members[id]
		r, e := s.exchange(s.ctx, m, sessionHello{Self: s.self, Members: wire.Members}, allow, a.addEvent)
		result.Downloaded += r.Downloaded
		result.Uploaded += r.Uploaded
		result.Deleted += r.Deleted
		result.Conflicts += r.Conflicts
		result.Examined += r.Examined
		result.Renames += r.Renames
		result.BatchDownloads += r.BatchDownloads
		s.mu.Lock()
		st := MemberStatus{ID: id, Name: m.Name, State: "Connected", LastSeen: time.Now()}
		if e != nil {
			st = s.memberStatus[id]
			st.ID = id
			st.Name = m.Name
			st.Error = e.Error()
			st.State = "Pending / unavailable"
			s.nextTry[id] = time.Now().Add(10 * time.Second)
			offline++
			// Connectivity is shown per member. A safety/integrity/application failure
			// must also reach the global error and approval controls.
			var hs *httpStatusError
			if !transientTransferError(e) && (!errors.As(e, &hs) || hs.Status != 404) {
				problems = append(problems, e)
			}
		} else {
			delete(s.nextTry, id)
		}
		s.memberStatus[id] = st
		s.publishMembers()
		s.mu.Unlock()
	}
	if s.ctx.Err() != nil {
		return
	}
	err = errors.Join(problems...)
	stats := s.scanner.Stats()
	a.mu.Lock()
	a.lastSync = time.Now()
	a.lastResult = result
	a.lastStats = stats
	previous := a.lastError
	a.lastError = ""
	a.state = "Up to date with reachable members"
	if len(ids) == 0 {
		a.state = "Session ready — invite another computer"
	} else if offline > 0 {
		a.state = fmt.Sprintf("Waiting for %d member(s); reachable members reconciled", offline)
	}
	if err != nil {
		a.lastError = err.Error()
		a.state = "Partial sync — will retry"
	}
	a.mu.Unlock()
	if err != nil && err.Error() != previous {
		a.notify("FolderBridge sync issue", err.Error(), true)
	} else if result.Conflicts > 0 {
		a.notify("FolderBridge conflict", fmt.Sprintf("Preserved %d conflict copy/copies", result.Conflicts), true)
	}
}
