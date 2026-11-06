package app

import (
	"errors"
	"folderbridge/internal/syncengine"
)

func (c config) engineOptions() syncengine.Options {
	return syncengine.Options{Mode: c.Mode, Folder: c.Folder, Invite: c.Invite, Secret: c.Secret, Session: c.Session, Name: c.Name, Enabled: c.Enabled, Paused: c.Paused}
}
func (a *application) startPeer() error {
	a.mu.RLock()
	cfg := a.cfg.engineOptions()
	cfg.ListenAddress = a.listenAddress
	a.mu.RUnlock()
	return a.engine.Start(cfg)
}
func (a *application) stopPeer()    { a.engine.Stop() }
func (a *application) requestSync() { a.engine.RequestSync() }

func (a *application) setPaused(paused bool) error {
	a.controlMu.Lock()
	defer a.controlMu.Unlock()
	a.mu.RLock()
	cfg := a.cfg
	a.mu.RUnlock()
	if !cfg.Enabled || cfg.Mode != "session" {
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
		a.mu.Lock()
		a.cfg.Paused = cfg.Paused
		a.mu.Unlock()
		if paused {
			_ = a.startPeer()
		}
		return err
	}
	if err := a.startPeer(); err != nil {
		a.setError(err.Error())
		return err
	}
	if paused {
		a.engine.RecordEvent("Synchronization paused")
	} else {
		a.engine.RecordEvent("Synchronization resumed")
	}
	return nil
}
