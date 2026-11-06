package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"folderbridge/internal/fileops"
	"folderbridge/internal/syncengine"
	"folderbridge/internal/update"
)

type config struct {
	Mode    string           `json:"mode"` // "host", "join" or "off"
	Folder  string           `json:"folder"`
	Invite  string           `json:"invite,omitempty"`
	Secret  string           `json:"secret,omitempty"`
	Enabled bool             `json:"enabled"`
	Paused  bool             `json:"paused,omitempty"`
	Updates *update.Settings `json:"updates,omitempty"`
}

func (a *application) saveConfig() error {
	data, err := json.MarshalIndent(a.cfg, "", "  ")
	if err != nil {
		return err
	}
	return fileops.WriteAtomically(filepath.Join(a.home, "config.json"), data)
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
	if err := fileops.RejectPathLinks(folder); err != nil {
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
	if err := fileops.CheckSyncRoot(folder); err != nil {
		return "", err
	}
	return folder, nil
}

func (a *application) configure(req config) error {
	a.controlMu.Lock()
	defer a.controlMu.Unlock()
	a.mu.RLock()
	draining := a.draining
	a.mu.RUnlock()
	if draining {
		return errors.New("update installation is in progress")
	}
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
		if err := syncengine.ValidateInvite(req.Invite); err != nil {
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
	// Sync configuration requests never overwrite independently managed updates.
	req.Updates = a.cfg.Updates
	a.cfg = req
	err := a.saveConfig()
	a.mu.Unlock()
	if err != nil {
		return err
	}
	return a.startPeer()
}
