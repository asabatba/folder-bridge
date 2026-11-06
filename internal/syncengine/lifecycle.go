package syncengine

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"folderbridge/internal/fileops"
	"net"
	"net/http"
	"path/filepath"
	"time"
)

// Start initializes a host/join session. Call Stop before changing an active
// session. Paused/disabled options establish status without opening a peer port.
func (a *Engine) Start(cfg Options) error {
	a.controlMu.Lock()
	defer a.controlMu.Unlock()
	a.mu.Lock()
	if a.draining {
		a.mu.Unlock()
		return errors.New("update installation is in progress")
	}
	a.cfg = cfg
	a.lastError = ""
	a.mu.Unlock()
	if !cfg.Enabled || cfg.Mode == "off" || cfg.Paused {
		a.mu.Lock()
		if cfg.Paused && cfg.Enabled && cfg.Mode != "off" {
			a.state = "Paused"
		} else {
			a.state = "Stopped"
		}
		a.mu.Unlock()
		return nil
	}
	if cfg.CheckBaseline && cfg.Mode == "join" {
		if _, err := loadBaseline(baselineFilename(a.home, cfg.Folder, cfg.Invite)); err != nil {
			return fmt.Errorf("updated engine cannot load synchronization baseline: %w", err)
		}
	}
	if err := fileops.CheckSyncRoot(cfg.Folder); err != nil {
		return fmt.Errorf("unsafe sync folder: %w", err)
	}
	if cleaned, err := cleanupStaleParts(cfg.Folder, time.Now()); err != nil {
		a.addEvent("Partial-file cleanup skipped: " + err.Error())
	} else if cleaned > 0 {
		a.addEvent(fmt.Sprintf("Recovered from interrupted transfers: removed %d stale temporary files", cleaned))
	}
	if cfg.Mode == "host" {
		cert, pin, err := newTLSCert(filepath.Join(a.home, "peer-cert.pem"), filepath.Join(a.home, "peer-key.pem"))
		if err != nil {
			return err
		}
		ln, err := net.Listen("tcp", ":"+peerPort)
		if err != nil {
			return fmt.Errorf("cannot listen on port %s: %w", peerPort, err)
		}
		host := newHost(cfg.Folder, cfg.Secret)
		host.scanner = newPersistentScanner(cfg.Folder, a.home)
		watchCtx, watchCancel := context.WithCancel(context.Background())
		a.mu.Lock()
		a.cancel = watchCancel
		a.mu.Unlock()
		go a.localChangeMonitor(watchCtx, cfg.Folder, host.scanner, host.signalChange)
		host.onEvent = a.addEvent
		host.onConnected = func() {
			a.mu.Lock()
			if a.state == "Hosting folder — waiting for peer" {
				a.state = "Hosting folder — peer connected"
			}
			a.mu.Unlock()
			a.addEvent("Paired computer connected")
		}
		server := &http.Server{Handler: a.hostDuringUpdate(host.handler()), ReadHeaderTimeout: 10 * time.Second}
		a.mu.Lock()
		a.fingerprint = pin
		a.hostScanner = host.scanner
		a.hostServer = server
		a.state = "Hosting folder — waiting for peer"
		a.mu.Unlock()
		go func() {
			err := server.Serve(tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13}))
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				a.ReportError("Host server: " + err.Error())
			}
		}()
		return nil
	}
	info, err := parseInvite(cfg.Invite)
	if err != nil {
		return err
	}
	p := newPeer(info)
	ctx, cancel := context.WithCancel(context.Background())
	a.mu.Lock()
	a.cancel = cancel
	a.state = "Connecting to peer"
	a.mu.Unlock()
	scanner := newPersistentScanner(cfg.Folder, a.home)
	go a.clientLoopWithScanner(ctx, cfg, p, scanner)
	return nil
}

// Stop cancels synchronization and closes the owned host listener. Update
// installation must first use Quiesce so admitted writes finish before shutdown.
func (a *Engine) Stop() {
	a.controlMu.Lock()
	defer a.controlMu.Unlock()
	a.mu.Lock()
	cancel, server := a.cancel, a.hostServer
	a.cancel, a.hostServer = nil, nil
	a.fingerprint = ""
	a.hostScanner = nil
	a.state = "Stopped"
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if server != nil {
		ctx, done := context.WithTimeout(context.Background(), 2*time.Second)
		server.Shutdown(ctx)
		done()
		server.Close()
	}
}

// RequestSync coalesces a manual request with filesystem/remote notifications.
func (a *Engine) RequestSync() {
	select {
	case a.syncSignal <- struct{}{}:
	default:
	}
}
func (a *Engine) addEvent(msg string) {
	a.mu.Lock()
	a.events = append([]string{time.Now().Format("15:04:05") + "  " + msg}, a.events...)
	if len(a.events) > 20 {
		a.events = a.events[:20]
	}
	a.mu.Unlock()
}
func (a *Engine) clientLoopWithScanner(ctx context.Context, cfg Options, p *peer, scanner *scanner) {
	defer p.Close()
	go a.localChangeMonitor(ctx, cfg.Folder, scanner, a.RequestSync)
	go a.remoteChangeMonitor(ctx, p)
	a.runSync(ctx, cfg, p, scanner)
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-a.syncSignal:
			if !debounceChanges(ctx, a.syncSignal, 350*time.Millisecond, 2*time.Second) {
				return
			}
		}
		a.runSync(ctx, cfg, p, scanner)
	}
}
func (a *Engine) runSync(ctx context.Context, cfg Options, p *peer, scanner *scanner) {
	a.syncMu.Lock()
	defer a.syncMu.Unlock()
	if ctx.Err() != nil {
		return
	}
	a.mu.Lock()
	if a.draining {
		a.mu.Unlock()
		return
	}
	a.state = "Synchronizing"
	a.mu.Unlock()
	filename := baselineFilename(a.home, cfg.Folder, cfg.Invite)
	base, err := loadBaseline(filename)
	if err != nil {
		a.mu.Lock()
		a.syncPersistErr = err
		a.mu.Unlock()
		a.ReportError("Baseline: " + err.Error())
		return
	}
	a.mu.Lock()
	allowMassDelete := a.allowMassDelete
	a.allowMassDelete = false
	a.mu.Unlock()
	next, result, syncErr := syncFolders(ctx, cfg.Folder, p, base, scanner, allowMassDelete, a.addEvent)
	if ctx.Err() != nil {
		return
	}
	if err := saveBaseline(filename, next); err != nil {
		a.mu.Lock()
		a.syncPersistErr = err
		a.mu.Unlock()
		a.ReportError("Save baseline: " + err.Error())
		return
	}
	a.mu.Lock()
	a.syncPersistErr = nil
	a.lastResult = result
	a.lastStats = scanner.Stats()
	a.lastSync = time.Now()
	previousError := a.lastError
	if syncErr != nil {
		a.lastError = syncErr.Error()
		a.state = "Partial sync — will retry"
	} else {
		a.lastError = ""
		a.state = "Up to date"
	}
	a.mu.Unlock()
	if syncErr != nil && syncErr.Error() != previousError {
		a.notify("FolderBridge sync issue", syncErr.Error(), true)
	} else if result.Conflicts > 0 {
		a.notify("FolderBridge conflict", fmt.Sprintf("Saved %d conflict copy/copies. Review your synced folder.", result.Conflicts), true)
	}
}
