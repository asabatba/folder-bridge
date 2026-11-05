package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const uiAddress = "127.0.0.1:47831"

type Config struct {
	Mode    string `json:"mode"` // "host", "join" or "off"
	Folder  string `json:"folder"`
	Invite  string `json:"invite,omitempty"`
	Secret  string `json:"secret,omitempty"`
	Enabled bool   `json:"enabled"`
	Paused  bool   `json:"paused,omitempty"`
}

type App struct {
	mu              sync.RWMutex
	controlMu       sync.Mutex
	syncMu          sync.Mutex
	home            string
	cfg             Config
	state           string
	lastError       string
	lastSync        time.Time
	lastResult      SyncResult
	lastStats       ScanStats
	hostScanner     *Scanner
	allowMassDelete bool
	events          []string
	fingerprint     string
	hostServer      *http.Server
	cancel          context.CancelFunc
	syncSignal      chan struct{}
	quit            chan struct{}
	quitOnce        sync.Once
	notifyFn        func(title, message string, warning bool)
}

func newApp(home string) (*App, error) {
	app := &App{home: home, state: "Stopped", syncSignal: make(chan struct{}, 1), quit: make(chan struct{})}
	data, err := os.ReadFile(filepath.Join(home, "config.json"))
	if err == nil {
		if err := json.Unmarshal(data, &app.cfg); err != nil {
			return nil, fmt.Errorf("read config: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return app, nil
}

func (a *App) saveConfig() error {
	data, err := json.MarshalIndent(a.cfg, "", "  ")
	if err != nil {
		return err
	}
	return writeJSONAtomically(filepath.Join(a.home, "config.json"), data)
}

func validateFolder(raw, home string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", errors.New("choose a folder first")
	}
	folder, err := filepath.Abs(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	if filepath.Dir(folder) == folder {
		return "", errors.New("choose a dedicated folder, not an entire drive")
	}
	if err := rejectPathLinks(folder); err != nil {
		return "", err
	}
	// Resolve the settings location too: AppData itself may use an alias.
	privateHome, err := filepath.EvalSymlinks(home)
	if err != nil {
		return "", fmt.Errorf("check private settings directory: %w", err)
	}
	// Application secrets must never be accidentally synced.
	if rel, err := filepath.Rel(folder, privateHome); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", errors.New("this folder includes FolderBridge private settings; select a narrower directory")
	}
	if err := os.MkdirAll(folder, 0755); err != nil {
		return "", err
	}
	if err := checkSyncRoot(folder); err != nil {
		return "", err
	}
	return folder, nil
}

func (a *App) configure(req Config) error {
	a.controlMu.Lock()
	defer a.controlMu.Unlock()
	if req.Mode != "host" && req.Mode != "join" && req.Mode != "off" {
		return errors.New("choose Host, Join, or Stop")
	}
	if req.Enabled && req.Mode != "off" {
		folder, err := validateFolder(req.Folder, a.home)
		if err != nil {
			return err
		}
		req.Folder = folder
	}
	if req.Mode == "join" && req.Enabled {
		if _, err := parseInvite(req.Invite); err != nil {
			return err
		}
	}
	a.stopPeer()
	if req.Mode == "host" {
		// Do not regenerate the token when restarting the same shared folder.
		a.mu.RLock()
		secret := a.cfg.Secret
		a.mu.RUnlock()
		if secret == "" {
			var err error
			secret, err = newToken()
			if err != nil {
				return err
			}
		}
		req.Secret = secret
	} else {
		a.mu.RLock()
		req.Secret = a.cfg.Secret
		a.mu.RUnlock()
	}
	a.mu.Lock()
	a.cfg = req
	a.lastError = ""
	err := a.saveConfig()
	a.mu.Unlock()
	if err != nil {
		return err
	}
	return a.startPeer()
}

func (a *App) startPeer() error {
	a.mu.RLock()
	cfg := a.cfg
	a.mu.RUnlock()
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
	if err := checkSyncRoot(cfg.Folder); err != nil {
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
		host.scanner = NewPersistentScanner(cfg.Folder, a.home)
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
		server := &http.Server{Handler: host.handler(), ReadHeaderTimeout: 10 * time.Second}
		a.mu.Lock()
		a.fingerprint = pin
		a.hostScanner = host.scanner
		a.hostServer = server
		a.state = "Hosting folder — waiting for peer"
		a.mu.Unlock()
		go func() {
			err := server.Serve(tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13}))
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				a.setError("Host server: " + err.Error())
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
	go a.clientLoop(ctx, cfg, p)
	return nil
}

func (a *App) stopPeer() {
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

func (a *App) setError(msg string) {
	a.mu.Lock()
	changed := a.lastError != msg
	a.lastError = msg
	a.state = "Error — will retry"
	a.mu.Unlock()
	if changed {
		a.notify("FolderBridge error", msg, true)
	}
}

func (a *App) setNotifier(fn func(string, string, bool)) {
	a.mu.Lock()
	a.notifyFn = fn
	a.mu.Unlock()
}

func (a *App) notify(title, msg string, warning bool) {
	a.mu.RLock()
	fn := a.notifyFn
	a.mu.RUnlock()
	if fn != nil {
		fn(title, msg, warning)
	}
}

// Pause is persisted, keeps pairing intact, and cancels any in-flight sync.
// Pausing the host also closes its listening socket.
func (a *App) setPaused(paused bool) error {
	a.controlMu.Lock()
	defer a.controlMu.Unlock()
	a.mu.RLock()
	cfg := a.cfg
	a.mu.RUnlock()
	if !cfg.Enabled || cfg.Mode == "off" {
		return errors.New("configure and start a folder before using pause")
	}
	if cfg.Paused == paused {
		return nil
	}
	if paused {
		a.stopPeer()
	}
	a.mu.Lock()
	a.cfg.Paused = paused
	err := a.saveConfig()
	a.mu.Unlock()
	if err != nil {
		// Restore the previous state when config cannot be saved.
		a.mu.Lock()
		a.cfg.Paused = cfg.Paused
		a.mu.Unlock()
		if paused {
			_ = a.startPeer()
		}
		return err
	}
	if paused {
		a.mu.Lock()
		a.state = "Paused"
		a.lastError = ""
		a.mu.Unlock()
		a.addEvent("Synchronization paused")
	} else {
		if err := a.startPeer(); err != nil {
			a.setError(err.Error())
			return err
		}
		a.mu.Lock()
		a.lastError = ""
		a.mu.Unlock()
		a.addEvent("Synchronization resumed")
	}
	return nil
}

func (a *App) requestSync() {
	select {
	case a.syncSignal <- struct{}{}:
	default:
	}
}

func (a *App) addEvent(msg string) {
	a.mu.Lock()
	a.events = append([]string{time.Now().Format("15:04:05") + "  " + msg}, a.events...)
	if len(a.events) > 20 {
		a.events = a.events[:20]
	}
	a.mu.Unlock()
}

func (a *App) clientLoop(ctx context.Context, cfg Config, p *Peer) {
	defer p.Close()
	scanner := NewPersistentScanner(cfg.Folder, a.home)
	go a.localChangeMonitor(ctx, cfg.Folder, scanner, func() {
		select {
		case a.syncSignal <- struct{}{}:
		default:
		}
	})
	go a.remoteChangeMonitor(ctx, p)
	a.runSync(ctx, cfg, p, scanner)
	// Periodic reconciliation is mandatory: filesystem notifications can be lost.
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

func (a *App) runSync(ctx context.Context, cfg Config, p *Peer, scanner *Scanner) {
	a.syncMu.Lock()
	defer a.syncMu.Unlock()
	if ctx.Err() != nil {
		return
	}
	a.mu.Lock()
	a.state = "Synchronizing"
	a.mu.Unlock()
	filename := baselineFilename(a.home, cfg.Folder, cfg.Invite)
	base, err := loadBaseline(filename)
	if err != nil {
		a.setError("Baseline: " + err.Error())
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
		a.setError("Save baseline: " + err.Error())
		return
	}
	a.mu.Lock()
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

func (a *App) status() any {
	startup := autostartEnabled()
	a.mu.RLock()
	defer a.mu.RUnlock()
	links := []string{}
	if a.cfg.Mode == "host" && a.cfg.Enabled && a.fingerprint != "" {
		for _, addr := range lanAddresses() {
			links = append(links, formatInvite(addr, a.cfg.Secret, a.fingerprint))
		}
	}
	copyEvents := append([]string{}, a.events...)
	stats := a.lastStats
	if a.hostScanner != nil {
		stats = a.hostScanner.Stats()
	}
	return struct {
		Mode      string     `json:"mode"`
		Folder    string     `json:"folder"`
		Invite    string     `json:"invite"`
		Enabled   bool       `json:"enabled"`
		Paused    bool       `json:"paused"`
		Autostart bool       `json:"autostart"`
		State     string     `json:"state"`
		Error     string     `json:"error"`
		LastSync  string     `json:"lastSync"`
		Result    SyncResult `json:"result"`
		Index     ScanStats  `json:"index"`
		Links     []string   `json:"links"`
		Events    []string   `json:"events"`
	}{a.cfg.Mode, a.cfg.Folder, a.cfg.Invite, a.cfg.Enabled, a.cfg.Paused, startup, a.state, a.lastError, a.lastSync.Format("2006-01-02 15:04:05"), a.lastResult, stats, links, copyEvents}
}

func (a *App) isPaused() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.cfg.Paused
}

func (a *App) uiHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		fmt.Fprint(w, pageHTML)
	})
	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(a.status())
	})
	jsonAction := func(fn func(http.ResponseWriter, *http.Request)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Content-Type") != "application/json" {
				http.Error(w, "expected application/json", http.StatusUnsupportedMediaType)
				return
			}
			if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+uiAddress {
				http.Error(w, "invalid origin", http.StatusForbidden)
				return
			}
			fn(w, r)
		}
	}
	mux.HandleFunc("GET /api/ignore", func(w http.ResponseWriter, r *http.Request) {
		a.mu.RLock()
		cfg := a.cfg
		a.mu.RUnlock()
		if cfg.Folder == "" || cfg.Mode == "off" || !cfg.Enabled {
			http.Error(w, "configure a sync folder first", http.StatusConflict)
			return
		}
		rel := r.URL.Query().Get("path")
		if _, err := checkedPath(cfg.Folder, rel); err != nil {
			http.Error(w, "invalid relative path: "+err.Error(), http.StatusBadRequest)
			return
		}
		checker := newPathIgnoreChecker(cfg.Folder)
		dir := filepath.ToSlash(filepath.Dir(rel))
		if dir == "." {
			dir = ""
		}
		matcher, err := checker.matcher(dir)
		if err != nil {
			http.Error(w, "local ignore policy: "+err.Error(), http.StatusConflict)
			return
		}
		local := matcher.Explain(rel, false)
		reply := struct {
			Local        IgnoreDecision  `json:"local"`
			Remote       *IgnoreDecision `json:"remote,omitempty"`
			Effective    bool            `json:"effective"`
			RemoteStatus string          `json:"remoteStatus"`
		}{Local: local, Effective: local.Ignored, RemoteStatus: "Unavailable in Host mode; inspect on the joining PC for both policies"}
		if cfg.Mode == "join" {
			reply.RemoteStatus = "Unavailable"
			if invite, err := parseInvite(cfg.Invite); err == nil {
				p := newPeer(invite)
				ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
				manifest, err := p.manifest(ctx)
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
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(reply)
	})
	mux.HandleFunc("POST /api/config", jsonAction(func(w http.ResponseWriter, r *http.Request) {
		var cfg Config
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&cfg); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if err := a.configure(cfg); err != nil {
			a.setError(err.Error())
			http.Error(w, err.Error(), 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true}`)
	}))
	mux.HandleFunc("POST /api/sync", jsonAction(func(w http.ResponseWriter, r *http.Request) {
		if a.isPaused() {
			http.Error(w, "resume synchronization first", 409)
			return
		}
		a.requestSync()
		fmt.Fprint(w, `{"ok":true}`)
	}))
	mux.HandleFunc("POST /api/pause", jsonAction(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Paused bool `json:"paused"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if err := a.setPaused(req.Paused); err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		fmt.Fprint(w, `{"ok":true}`)
	}))
	mux.HandleFunc("POST /api/autostart", jsonAction(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if err := setAutostart(req.Enabled); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		fmt.Fprint(w, `{"ok":true}`)
	}))
	mux.HandleFunc("POST /api/approve-bulk", jsonAction(func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		a.allowMassDelete = true
		a.mu.Unlock()
		select {
		case a.syncSignal <- struct{}{}:
		default:
		}
		fmt.Fprint(w, `{"ok":true}`)
	}))
	mux.HandleFunc("POST /api/quit", jsonAction(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ok":true}`)
		a.quitOnce.Do(func() { close(a.quit) })
	}))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != uiAddress {
			http.Error(w, "invalid dashboard host; use http://"+uiAddress, http.StatusForbidden)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func openBrowser() {
	if runtime.GOOS == "windows" {
		exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", "http://"+uiAddress+"/").Start()
	}
}

func main() {
	background := flag.Bool("background", false, "start minimized in the system tray")
	flag.Parse()
	home, err := configHome()
	if err != nil {
		log.Fatal(err)
	}
	if err := os.MkdirAll(home, 0700); err != nil {
		log.Fatal(err)
	}
	app, err := newApp(home)
	if err != nil {
		log.Fatal(err)
	}
	listener, err := net.Listen("tcp", uiAddress)
	if err != nil {
		openBrowser()
		return /* already running or port occupied */
	}
	uiServer := &http.Server{Handler: app.uiHandler(), ReadHeaderTimeout: 10 * time.Second}
	go uiServer.Serve(listener)
	stopTray := startTray(app)
	if err := app.startPeer(); err != nil {
		app.setError(err.Error())
	}
	if !*background {
		openBrowser()
	}
	<-app.quit
	stopTray()
	app.stopPeer()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	uiServer.Shutdown(ctx)
}
