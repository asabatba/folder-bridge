package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"time"
)

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
	a.updateRoutes(mux, jsonAction)
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
