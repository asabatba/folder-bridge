package app

import (
	"encoding/json"
	"fmt"
	"folderbridge/internal/fileops"
	"folderbridge/internal/syncengine"
	"net/http"
)

func (a *application) status() any {
	startup := autostartEnabled()
	a.mu.RLock()
	defer a.mu.RUnlock()
	status := a.engine.Snapshot()
	return struct {
		Session   string                    `json:"session"`
		Name      string                    `json:"name"`
		Members   []syncengine.MemberStatus `json:"members"`
		Legacy    bool                      `json:"legacy"`
		Version   string                    `json:"version"`
		Mode      string                    `json:"mode"`
		Folder    string                    `json:"folder"`
		Invite    string                    `json:"invite"`
		Enabled   bool                      `json:"enabled"`
		Paused    bool                      `json:"paused"`
		Autostart bool                      `json:"autostart"`
		State     string                    `json:"state"`
		Error     string                    `json:"error"`
		LastSync  string                    `json:"lastSync"`
		Result    syncengine.SyncResult     `json:"result"`
		Index     syncengine.ScanStats      `json:"index"`
		Links     []string                  `json:"links"`
		Events    []string                  `json:"events"`
	}{Session: a.cfg.Session, Name: a.cfg.Name, Members: status.Members, Legacy: a.cfg.Mode == "host" || a.cfg.Mode == "join", Version: Version, Mode: a.cfg.Mode, Folder: a.cfg.Folder, Invite: "", Enabled: a.cfg.Enabled, Paused: a.cfg.Paused, Autostart: startup, State: status.State, Error: status.Error, LastSync: status.LastSync.Format("2006-01-02 15:04:05"), Result: status.Result, Index: status.Index, Links: status.Links, Events: status.Events}
}

func (a *application) isPaused() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.cfg.Paused
}

func (a *application) uiHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
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
		if cfg.Folder == "" || cfg.Mode != "session" || !cfg.Enabled {
			http.Error(w, "configure a sync folder first", http.StatusConflict)
			return
		}
		rel := r.URL.Query().Get("path")
		if _, err := fileops.CheckedPath(cfg.Folder, rel); err != nil {
			http.Error(w, "invalid relative path: "+err.Error(), http.StatusBadRequest)
			return
		}
		reply, err := syncengine.ExplainIgnore(r.Context(), cfg.engineOptions(), rel)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(reply)
	})
	mux.HandleFunc("POST /api/config", jsonAction(func(w http.ResponseWriter, r *http.Request) {
		var cfg config
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
		a.engine.ApproveBulkDelete()
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
