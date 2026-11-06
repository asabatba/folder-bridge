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
)

type config struct {
	Mode    string `json:"mode"` // "host", "join" or "off"
	Folder  string `json:"folder"`
	Invite  string `json:"invite,omitempty"`
	Secret  string `json:"secret,omitempty"`
	Enabled bool   `json:"enabled"`
	Paused  bool   `json:"paused,omitempty"`
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
	// Compare filesystem identities, not path spellings: Windows 8.3 names
	// and extended-length prefixes can identify the same directory while Rel
	// reports unrelated paths. An absent folder cannot contain the existing home.
	folderInfo, err := os.Stat(folder)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("check selected folder: %w", err)
	}
	if err == nil {
		for ancestor := privateHome; ; ancestor = filepath.Dir(ancestor) {
			info, err := os.Stat(ancestor)
			if err != nil {
				return "", fmt.Errorf("check private settings ancestor: %w", err)
			}
			if os.SameFile(folderInfo, info) {
				return "", errors.New("this folder includes FolderBridge private settings; select a narrower directory")
			}
			if filepath.Dir(ancestor) == ancestor {
				break
			}
		}
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
	a.cfg = req
	err := a.saveConfig()
	a.mu.Unlock()
	if err != nil {
		return err
	}
	return a.startPeer()
}
