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
	Mode    string `json:"mode"` // "session" or "off"; old roles require fresh pairing
	Folder  string `json:"folder"`
	Invite  string `json:"invite,omitempty"`
	Secret  string `json:"secret,omitempty"`
	Session string `json:"session,omitempty"`
	Name    string `json:"name,omitempty"`
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
	a.mu.RLock()
	old := a.cfg
	a.mu.RUnlock()
	next := old
	switch req.Mode {
	case "refresh":
		return a.engine.RefreshMemberInvite(req.Invite)
	case "off":
		next.Enabled = false
		next.Paused = false
	case "leave":
		next = config{Mode: "off", Folder: old.Folder, Name: old.Name}
	case "session":
		if old.Mode != "session" || old.Session == "" {
			return errors.New("create or join a session first")
		}
		if req.Folder != "" && req.Folder != old.Folder {
			return errors.New("leave this session before choosing another folder")
		}
		next.Enabled = true
		next.Paused = false
	case "create", "join":
		if old.Mode == "session" && old.Session != "" {
			return errors.New("leave the current session before creating or joining another")
		}
		folder, err := validateFolder(req.Folder, a.home)
		if err != nil {
			return err
		}
		invite := ""
		if req.Mode == "join" {
			invite = strings.TrimSpace(req.Invite)
			if err := syncengine.ValidateInvite(invite); err != nil {
				return err
			}
		}
		credentials, err := a.engine.PrepareSession(folder, invite)
		if err != nil {
			return err
		}
		// Preserve legacy settings for a deliberate manual rollback. No old baseline
		// is imported, renamed, deleted or used as a new session's deletion history.
		if old.Mode == "host" || old.Mode == "join" {
			backup := filepath.Join(a.home, "legacy-config.json")
			if _, err := os.Stat(backup); errors.Is(err, os.ErrNotExist) {
				data, err := json.MarshalIndent(old, "", "  ")
				if err != nil {
					return err
				}
				if err = fileops.WriteAtomically(backup, data); err != nil {
					return err
				}
			} else if err != nil {
				return err
			}
		}
		name := strings.TrimSpace(req.Name)
		if len(name) > 128 {
			return errors.New("computer name must be at most 128 bytes")
		}
		next = config{Mode: "session", Folder: folder, Session: credentials.ID, Secret: credentials.Secret, Name: name, Enabled: true}
	default:
		return errors.New("choose Create session, Join session, Resume, Stop, or Leave")
	}
	a.stopPeer()
	a.mu.Lock()
	a.cfg = next
	err := a.saveConfig()
	if err != nil {
		a.cfg = old
	}
	a.mu.Unlock()
	if err != nil {
		_ = a.startPeer()
		return err
	}
	return a.startPeer()
}
