package syncengine

import (
	"errors"
	"path/filepath"
)

// RefreshMemberInvite repairs a stale LAN address without discarding causal
// history or re-pairing. An invitation from a different session is never merged.
func (a *Engine) RefreshMemberInvite(raw string) error {
	invite, err := parseSessionInvite(raw)
	if err != nil {
		return err
	}
	a.controlMu.Lock()
	defer a.controlMu.Unlock()
	a.mu.RLock()
	cfg, s := a.cfg, a.session
	a.mu.RUnlock()
	if cfg.Mode != "session" || invite.ID != cfg.Session || invite.Secret != cfg.Secret {
		return errors.New("use a current invitation from this same session")
	}
	if s != nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		if invite.Fingerprint == s.self.ID {
			return errors.New("paste an invitation from another member")
		}
		member := s.state.Members[invite.Fingerprint]
		member.ID = invite.Fingerprint
		if member.Name == "" {
			member.Name = "Session member"
		}
		member.Addresses = []string{invite.Address}
		s.state.Members[member.ID] = member
		delete(s.nextTry, member.ID)
		if err := saveSessionState(s.filename, s.state); err != nil {
			return err
		}
	} else {
		_, pin, err := newTLSCert(filepath.Join(a.home, "peer-cert.pem"), filepath.Join(a.home, "peer-key.pem"))
		if err != nil {
			return err
		}
		if invite.Fingerprint == pin {
			return errors.New("paste an invitation from another member")
		}
		filename := filepath.Join(a.home, "session-"+cfg.Session+".json")
		state, err := loadSessionState(filename, cfg.Session, pin, cfg.Folder)
		if err != nil {
			return err
		}
		member := state.Members[invite.Fingerprint]
		member.ID = invite.Fingerprint
		if member.Name == "" {
			member.Name = "Session member"
		}
		member.Addresses = []string{invite.Address}
		state.Members[member.ID] = member
		if err := saveSessionState(filename, state); err != nil {
			return err
		}
	}
	a.RequestSync()
	return nil
}
