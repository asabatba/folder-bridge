package update

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

type Settings struct {
	Enabled            bool   `json:"enabled"`
	CheckIntervalHours int    `json:"checkIntervalHours"`
	AutoDownload       bool   `json:"autoDownload"`
	AutoInstall        bool   `json:"autoInstall"`
	Channel            string `json:"channel"`
	SkippedVersion     string `json:"skippedVersion"`
}

func DefaultSettings() Settings {
	return Settings{Enabled: true, CheckIntervalHours: 24, AutoDownload: true, Channel: "stable"}
}
func (s Settings) Validate() error {
	if s.CheckIntervalHours < 1 || s.CheckIntervalHours > 720 {
		return errors.New("update check interval must be 1–720 hours")
	}
	if s.AutoInstall {
		return errors.New("automatic installation is not supported; explicit approval is always required")
	}
	if s.Channel != "stable" {
		return errors.New("only the stable update channel is supported")
	}
	if s.SkippedVersion != "" {
		_, err := ParseVersion(s.SkippedVersion)
		return err
	}
	return nil
}

type Status struct {
	CurrentVersion     string    `json:"currentVersion"`
	AvailableVersion   string    `json:"availableVersion"`
	State              string    `json:"state"`
	ReleaseNotes       string    `json:"releaseNotes"`
	LastCheck          time.Time `json:"lastCheck"`
	LastResult         string    `json:"lastResult"`
	InstallationResult string    `json:"installationResult,omitempty"`
	FailedVersion      string    `json:"failedVersion"`
	Settings           Settings  `json:"settings"`
	Ready              bool      `json:"ready"`
}
type savedState struct {
	LastCheck       time.Time `json:"lastCheck"`
	LastResult      string    `json:"lastResult"`
	Stage           string    `json:"stage,omitempty"`
	RetriedResultID string    `json:"retriedResultID,omitempty"`
}
type Manager struct {
	mu              sync.Mutex
	home            string
	status          Status
	client          *http.Client
	stage           string
	resultID        string
	retriedResultID string
	busy            bool
	installing      bool
	cancel          context.CancelFunc
	done            chan struct{}
	opCancel        context.CancelFunc
	opDone          chan struct{}
	stopping        bool
	notify          func(string)
}

func NewManager(home string, notify func(string)) *Manager {
	m := &Manager{home: home, client: HTTPClient(), notify: notify, status: Status{CurrentVersion: Version, State: "idle"}}
	var saved savedState
	if readJSON(filepath.Join(home, "update-state.json"), &saved) == nil {
		m.status.LastCheck = saved.LastCheck
		m.status.LastResult = saved.LastResult
		m.retriedResultID = saved.RetriedResultID
	}
	var result Result
	if readJSON(filepath.Join(home, "update-result.json"), &result) == nil {
		m.resultID = result.ID
		if result.ID == "" || result.ID != m.retriedResultID {
			m.status.FailedVersion = result.FailedVersion
		}
		m.status.LastResult = result.Message
		m.status.InstallationResult = result.Message
	}
	// Reuse downloaded bytes across restarts only after revalidating signed
	// evidence, and never accept a state-file-selected path outside private staging.
	stage := filepath.Clean(saved.Stage)
	if saved.Stage != "" && filepath.Dir(stage) == filepath.Join(home, "updates") && strings.HasPrefix(filepath.Base(stage), "release-") {
		if manifest, err := VerifyStage(stage); err == nil {
			if cmp, e := Compare(manifest.Version, Version); e == nil && cmp > 0 && manifest.Version != m.status.FailedVersion {
				m.stage = stage
				m.status.Ready = true
				m.status.State = "ready"
				m.status.AvailableVersion = manifest.Version
				m.status.ReleaseNotes = "https://github.com/" + Repository + "/releases/tag/v" + manifest.Version
			}
		}
	}
	return m
}
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	// The helper confirms only after the new app has initialized. Refresh its
	// result on demand so a dashboard need not wait another restart to see it.
	var result Result
	if readJSON(filepath.Join(m.home, "update-result.json"), &result) == nil && result.ID != "" && result.ID != m.resultID {
		m.resultID = result.ID
		m.status.InstallationResult = result.Message
		m.status.FailedVersion = ""
		if result.ID != m.retriedResultID {
			m.status.FailedVersion = result.FailedVersion
		}
		if m.status.FailedVersion != "" && m.status.FailedVersion == m.status.AvailableVersion {
			m.status.Ready = false
			m.status.State = "failed version quarantined — explicit retry required"
		}
	}
	return m.status
}
func (m *Manager) Start(s Settings) {
	m.Stop()
	m.mu.Lock()
	m.status.Settings = s
	if s.SkippedVersion != "" && s.SkippedVersion == m.status.AvailableVersion {
		m.status.Ready = false
		m.status.AvailableVersion = ""
		m.status.ReleaseNotes = ""
		m.status.State = "version skipped"
	}
	if !s.Enabled {
		m.status.State = "disabled"
		m.mu.Unlock()
		return
	}
	if runtime.GOOS != "windows" {
		m.status.State = "unsupported platform"
		m.mu.Unlock()
		return
	}
	if _, err := Key(); err != nil {
		m.status.State = "unavailable"
		m.status.LastResult = err.Error()
		m.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	m.cancel = cancel
	m.done = done
	m.mu.Unlock()
	go func() {
		defer close(done)
		m.Check(ctx, false, false)
		timer := time.NewTicker(time.Duration(s.CheckIntervalHours) * time.Hour)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				m.Check(ctx, false, false)
			}
		}
	}()
}
func (m *Manager) Stop() {
	m.mu.Lock()
	cancel, done := m.cancel, m.done
	opCancel, opDone := m.opCancel, m.opDone
	m.stopping = true
	m.cancel = nil
	m.done = nil
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if opCancel != nil {
		opCancel()
	}
	if opDone != nil {
		<-opDone
	}
	if done != nil {
		<-done
	}
	m.mu.Lock()
	m.stopping = false
	m.mu.Unlock()
}

// Check is serialized, including manual requests. Explicit retry is the only way
// to try a quarantined version again; skipping does not silently retry it.
func (m *Manager) Check(ctx context.Context, download, retry bool) error {
	m.mu.Lock()
	if m.stopping || !m.status.Settings.Enabled {
		m.mu.Unlock()
		return errors.New("updates are disabled")
	}
	if m.busy || m.installing {
		m.mu.Unlock()
		return errors.New("an update operation is already in progress")
	}
	m.busy = true
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	m.opCancel = cancel
	m.opDone = done
	m.status.State = "checking"
	s := m.status.Settings
	m.mu.Unlock()
	defer func() {
		cancel()
		m.mu.Lock()
		m.busy = false
		m.opCancel = nil
		m.opDone = nil
		close(done)
		m.mu.Unlock()
	}()
	manifest, data, sig, err := Discover(ctx, m.client)
	m.mu.Lock()
	m.status.LastCheck = time.Now()
	m.mu.Unlock()
	if err != nil {
		return m.fail(err)
	}
	cmp, err := Compare(manifest.Version, Version)
	if err != nil {
		return m.fail(err)
	}
	m.mu.Lock()
	failed := m.status.FailedVersion
	if cmp <= 0 || manifest.Version == s.SkippedVersion || (manifest.Version == failed && !retry) {
		m.status.Ready = false
		m.status.AvailableVersion = ""
		m.status.ReleaseNotes = ""
		m.status.State = "up to date"
		if manifest.Version == s.SkippedVersion {
			m.status.State = "version skipped"
		}
		if manifest.Version == failed {
			m.status.State = "failed version quarantined — explicit retry required"
		}
		m.status.LastResult = m.status.State
		m.saveLocked()
		m.mu.Unlock()
		return nil
	}
	if retry && manifest.Version == failed {
		// Retry approval is scoped to this failed transaction. A second failed
		// installation of the same version has a new ID and is quarantined again.
		m.retriedResultID = m.resultID
		m.status.FailedVersion = ""
	}
	readyVersion := m.status.AvailableVersion
	m.status.AvailableVersion = manifest.Version
	m.status.ReleaseNotes = "https://github.com/" + Repository + "/releases/tag/v" + manifest.Version
	// Avoid repeated ready notifications/downloads on daily checks.
	if m.status.Ready && m.stage != "" && readyVersion == manifest.Version {
		m.status.State = "ready"
		m.saveLocked()
		m.mu.Unlock()
		return nil
	}
	m.status.Ready = false
	if !s.AutoDownload && !download {
		m.status.State = "available"
		m.status.LastResult = "Update available; automatic download is off"
		m.saveLocked()
		m.mu.Unlock()
		return nil
	}
	m.status.State = "downloading"
	m.mu.Unlock()
	if err = os.MkdirAll(filepath.Join(m.home, "updates"), 0700); err != nil {
		return m.fail(err)
	}
	stage, err := os.MkdirTemp(filepath.Join(m.home, "updates"), "release-")
	if err != nil {
		return m.fail(err)
	}
	keep := false
	defer func() {
		if !keep {
			os.RemoveAll(stage)
		}
	}()
	if err = Download(ctx, m.client, stage, manifest); err != nil {
		return m.fail(err)
	}
	if err = os.WriteFile(filepath.Join(stage, "manifest.json"), data, 0600); err != nil {
		return m.fail(err)
	}
	if err = os.WriteFile(filepath.Join(stage, "manifest.sig"), sig, 0600); err != nil {
		return m.fail(err)
	}
	if err = Extract(filepath.Join(stage, "release.zip"), stage); err != nil {
		return m.fail(err)
	}
	if err = ctx.Err(); err != nil {
		return m.fail(err)
	}
	m.mu.Lock()
	old := m.stage
	m.stage = stage
	m.status.State = "ready"
	m.status.Ready = true
	m.status.LastResult = "Update ready — Restart to install"
	m.saveLocked()
	m.mu.Unlock()
	keep = true
	if old != "" {
		os.RemoveAll(old)
	}
	if m.notify != nil {
		m.notify("Update ready — Restart to install v" + manifest.Version)
	}
	return nil
}
func (m *Manager) saveLocked() {
	if err := WriteJSON(filepath.Join(m.home, "update-state.json"), savedState{LastCheck: m.status.LastCheck, LastResult: m.status.LastResult, Stage: m.stage, RetriedResultID: m.retriedResultID}); err != nil {
		m.status.LastResult += "; cannot persist update status: " + err.Error()
	}
}
func (m *Manager) fail(err error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status.State = "error"
	m.status.LastResult = err.Error()
	m.saveLocked()
	return err
}
func (m *Manager) BeginInstall() (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.busy || m.installing || !m.status.Ready || m.stage == "" || !m.status.Settings.Enabled || m.status.AvailableVersion == m.status.Settings.SkippedVersion {
		return "", errors.New("no verified update is ready for installation")
	}
	m.installing = true
	m.status.State = "waiting for synchronization to finish"
	return m.stage, nil
}
func (m *Manager) InstallFailed(err error) {
	m.mu.Lock()
	m.installing = false
	m.mu.Unlock()
	m.fail(err)
}
