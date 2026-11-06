package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"folderbridge/internal/update"
)

type App struct {
	mu              sync.RWMutex
	controlMu       sync.Mutex
	syncMu          sync.Mutex
	requestGate     sync.RWMutex
	draining        bool
	syncPersistErr  error // protected by mu; blocks update if latest baseline couldn't persist
	updates         *update.Manager
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
	defaults := update.DefaultSettings()
	app.cfg.Updates = &defaults
	data, err := os.ReadFile(filepath.Join(home, "config.json"))
	if err == nil {
		if err := json.Unmarshal(data, &app.cfg); err != nil {
			return nil, fmt.Errorf("read config: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if app.cfg.Updates == nil {
		app.cfg.Updates = &defaults
	}
	if err := app.cfg.Updates.Validate(); err != nil {
		return nil, fmt.Errorf("update settings: %w", err)
	}
	app.updates = update.NewManager(home, func(msg string) { app.notify("FolderBridge update", msg, false) })
	return app, nil
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
