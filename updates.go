package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"folderbridge/internal/update"
)

func (a *App) startUpdates() { a.mu.RLock(); s := *a.cfg.Updates; a.mu.RUnlock(); a.updates.Start(s) }
func (a *App) stopUpdates()  { a.updates.Stop() }
func (a *App) updateSettings(s update.Settings) error {
	if err := s.Validate(); err != nil {
		return err
	}
	a.controlMu.Lock()
	defer a.controlMu.Unlock()
	a.mu.Lock()
	if a.draining {
		a.mu.Unlock()
		return errors.New("update installation is in progress")
	}
	old := a.cfg.Updates
	a.cfg.Updates = &s
	if err := a.saveConfig(); err != nil {
		a.cfg.Updates = old
		a.mu.Unlock()
		return err
	}
	a.mu.Unlock()
	a.updates.Start(s)
	return nil
}
func (a *App) skipUpdate() error {
	status := a.updates.Status()
	if status.AvailableVersion == "" {
		return errors.New("no available version to skip")
	}
	a.mu.RLock()
	s := *a.cfg.Updates
	a.mu.RUnlock()
	s.SkippedVersion = status.AvailableVersion
	return a.updateSettings(s)
}
func (a *App) checkUpdate(download, retry bool) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		if err := a.updates.Check(ctx, download, retry); err != nil {
			a.notify("FolderBridge update", err.Error(), true)
		}
	}()
}
func (a *App) approveUpdate() error {
	if runtime.GOOS != "windows" {
		return errors.New("automatic installation is supported only on Windows")
	}
	stage, err := a.updates.BeginInstall()
	if err != nil {
		return err
	}
	go func() {
		if err := a.installUpdate(stage); err != nil {
			a.updates.InstallFailed(err)
			a.notify("FolderBridge update", err.Error(), true)
		}
	}()
	return nil
}
func (a *App) installUpdate(stage string) error {
	a.controlMu.Lock()
	defer a.controlMu.Unlock()
	a.mu.Lock()
	a.draining = true
	a.mu.Unlock()
	committed := false
	defer func() {
		if !committed {
			a.mu.Lock()
			a.draining = false
			a.mu.Unlock()
			a.requestSync()
		}
	}()
	// Stop admitting new client cycles and peer requests, but don't cancel active
	// contexts. The last client cycle persists its baseline before releasing syncMu.
	a.syncMu.Lock()
	defer a.syncMu.Unlock()
	a.requestGate.Lock()
	defer a.requestGate.Unlock()
	a.mu.Lock()
	err := a.syncPersistErr
	if err == nil {
		err = a.saveConfig()
	}
	a.mu.Unlock()
	if err != nil {
		return fmt.Errorf("persist synchronization state before update: %w", err)
	}
	target, err := os.Executable()
	if err != nil {
		return err
	}
	if err = rejectPathLinks(target); err != nil {
		return err
	}
	plan := update.Plan{Stage: stage, Target: target, CurrentVersion: update.Version, ParentPID: os.Getpid(), Result: filepath.Join(a.home, "update-result.json")}
	planPath := filepath.Join(stage, "install-plan.json")
	if err = update.WriteJSON(planPath, plan); err != nil {
		return err
	}
	// Re-verify the archive and re-extract the helper immediately before execution.
	if _, err = update.VerifyStage(stage); err != nil {
		return err
	}
	for _, name := range []string{"helper-ready.json", "helper-error.txt"} {
		if err = os.Remove(filepath.Join(stage, name)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	helperDir, err := os.MkdirTemp(stage, "launch-")
	if err != nil {
		return err
	}
	if err = update.Extract(filepath.Join(stage, "release.zip"), helperDir); err != nil {
		return err
	}
	// Both the signature and ZIP digest were checked above; the executable is a
	// fresh extraction into our private staging directory, not a peer/user command.
	cmd := exec.Command(filepath.Join(helperDir, "FolderBridge-Updater.exe"), "--plan", planPath)
	if err = cmd.Start(); err != nil {
		return fmt.Errorf("cannot launch verified updater (device policy may prohibit it); upgrade manually from https://github.com/%s/releases: %w", update.Repository, err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	poll := time.NewTicker(100 * time.Millisecond)
	defer poll.Stop()
	for {
		select {
		case err = <-exited:
			// Helper records preparation errors without requiring a console window.
			if data, e := os.ReadFile(filepath.Join(stage, "helper-error.txt")); e == nil {
				return errors.New(string(data))
			}
			return fmt.Errorf("updater exited before safe handoff: %v", err)
		case <-timer.C:
			// It has not received permission to replace anything until this process exits.
			// Kill only our helper; keep the app running if preparation cannot complete.
			if err = cmd.Process.Kill(); err != nil {
				return fmt.Errorf("updater preparation timed out; cannot stop helper: %w", err)
			}
			<-exited
			return errors.New("updater preparation timed out; current app remains running; recover pending installation before retrying")
		case <-poll.C:
			if _, err = os.Stat(filepath.Join(stage, "helper-ready.json")); err == nil {
				committed = true
				a.quitOnce.Do(func() { close(a.quit) })
				return nil
			}
		}
	}
}

// hostDuringUpdate drains all admitted handlers, including writes and batches.
// A joining peer receives a transient error instead of starting another write.
func (a *App) hostDuringUpdate(next http.Handler) http.Handler {
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
func (a *App) updateRoutes(mux *http.ServeMux, jsonAction func(func(http.ResponseWriter, *http.Request)) http.HandlerFunc) {
	mux.HandleFunc("GET /api/updates", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(a.updates.Status())
	})
	action := func(path string, fn func() error) {
		mux.HandleFunc("POST /api/updates/"+path, jsonAction(func(w http.ResponseWriter, r *http.Request) {
			if err := fn(); err != nil {
				http.Error(w, err.Error(), 409)
				return
			}
			fmt.Fprint(w, `{"ok":true}`)
		}))
	}
	action("check", func() error { a.checkUpdate(false, false); return nil })
	action("download", func() error { a.checkUpdate(true, false); return nil })
	action("retry", func() error { a.checkUpdate(true, true); return nil })
	action("install", a.approveUpdate)
	action("skip", a.skipUpdate)
	mux.HandleFunc("POST /api/updates/settings", jsonAction(func(w http.ResponseWriter, r *http.Request) {
		s := update.DefaultSettings()
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&s); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if err := a.updateSettings(s); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		fmt.Fprint(w, `{"ok":true}`)
	}))
}
