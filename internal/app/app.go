// Package app owns process orchestration, settings and the local dashboard/tray.
// Synchronization internals live behind the engine module.
package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"folderbridge/internal/syncengine"
	"os"
	"path/filepath"
	"sync"
)

type application struct {
	mu        sync.RWMutex
	controlMu sync.Mutex
	engine    *syncengine.Engine
	home      string
	cfg       config
	quit      chan struct{}
	quitOnce  sync.Once
	notifyFn  func(string, string, bool)
}

func newApp(home string) (*application, error) {
	app := &application{home: home, quit: make(chan struct{})}
	data, err := os.ReadFile(filepath.Join(home, "config.json"))
	if err == nil {
		if err := json.Unmarshal(data, &app.cfg); err != nil {
			return nil, fmt.Errorf("read config: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	app.engine = syncengine.New(home, app.notify)
	return app, nil
}
func (a *application) setError(msg string) { a.engine.ReportError(msg) }
func (a *application) setNotifier(fn func(string, string, bool)) {
	a.mu.Lock()
	a.notifyFn = fn
	a.mu.Unlock()
}
func (a *application) notify(title, msg string, warning bool) {
	a.mu.RLock()
	fn := a.notifyFn
	a.mu.RUnlock()
	if fn != nil {
		fn(title, msg, warning)
	}
}
