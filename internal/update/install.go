package update

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const JournalName = ".folderbridge-update.json"
const HealthEnv = "FOLDERBRIDGE_UPDATE_HEALTH"
const TokenEnv = "FOLDERBRIDGE_UPDATE_TOKEN"

type Plan struct {
	Stage          string `json:"stage"`
	Target         string `json:"target"`
	CurrentVersion string `json:"currentVersion"`
	ParentPID      int    `json:"parentPID"`
	Result         string `json:"result"`
}
type Journal struct {
	Target        string `json:"target"`
	Work          string `json:"work"`
	Version       string `json:"version"`
	PreviousHash  string `json:"previousHash"`
	HelperHash    string `json:"helperHash"`
	CandidateHash string `json:"candidateHash"`
	Result        string `json:"result"`
	Phase         string `json:"phase"`
}
type Result struct {
	ID            string `json:"id"`
	Version       string `json:"version"`
	FailedVersion string `json:"failedVersion"`
	Message       string `json:"message"`
}

func WriteJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".update-json-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(name, path)
}
func readJSON(path string, value any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewDecoder(io.LimitReader(f, 64<<10)).Decode(value)
}
func copyExclusive(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0700)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if err == nil {
		err = out.Sync()
	}
	e := out.Close()
	if err != nil {
		return err
	}
	return e
}
func fileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	_, err = io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), err
}
func renameRetry(src, dst string) error {
	var err error
	for i := 0; i < 8; i++ {
		if err = os.Rename(src, dst); err == nil {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("cannot replace executable (permissions or sharing lock; no elevation attempted): %w", err)
}

// Prepare independently re-verifies the signed ZIP in the helper, and creates a
// same-volume durable backup BEFORE the live path can change. The live path is
// never removed first: sudden power loss leaves the old or new file plus backup.
func Prepare(p Plan) (Journal, error) {
	var j Journal
	target, err := filepath.Abs(p.Target)
	if err != nil {
		return j, err
	}
	if filepath.Base(target) != "FolderBridge.exe" {
		return j, errors.New("automatic updates require the filename FolderBridge.exe; upgrade manually")
	}
	if err = SafePath(target); err != nil {
		return j, err
	}
	m, err := VerifyStage(p.Stage)
	if err != nil {
		return j, err
	}
	cmp, err := Compare(m.Version, p.CurrentVersion)
	if err != nil || cmp <= 0 {
		return j, errors.New("update is not newer than installed version")
	}
	journalPath := filepath.Join(filepath.Dir(target), JournalName)
	if _, err = os.Lstat(journalPath); !errors.Is(err, os.ErrNotExist) {
		return j, errors.New("unfinished update exists; recover it before installing")
	}
	// Directory creation permission does not imply permission to create the
	// journal/live file on Windows ACLs. Probe both before preparing anything.
	probe, err := os.CreateTemp(filepath.Dir(target), ".folderbridge-write-check-")
	if err != nil {
		return j, fmt.Errorf("application directory is not writable; quit and manually upgrade from https://github.com/%s/releases (respect device policy): %w", Repository, err)
	}
	probeName := probe.Name()
	closeErr := probe.Close()
	removeErr := os.Remove(probeName)
	if closeErr != nil {
		return j, closeErr
	}
	if removeErr != nil {
		return j, fmt.Errorf("cannot remove application-directory write probe; manually upgrade: %w", removeErr)
	}
	work, err := os.MkdirTemp(filepath.Dir(target), ".folderbridge-install-")
	if err != nil {
		return j, fmt.Errorf("application directory is not writable; quit and manually upgrade from https://github.com/%s/releases (respect device policy): %w", Repository, err)
	}
	ok := false
	defer func() {
		if !ok {
			os.RemoveAll(work)
		}
	}()
	// Retain signed evidence on the application volume for independently verified
	// recovery after power loss, even if AppData staging is later unavailable.
	for _, name := range []string{"manifest.json", "manifest.sig", "release.zip"} {
		if err = copyExclusive(filepath.Join(p.Stage, name), filepath.Join(work, name)); err != nil {
			return j, err
		}
	}
	if _, err = VerifyStage(work); err != nil {
		return j, err
	}
	if err = Extract(filepath.Join(work, "release.zip"), work); err != nil {
		return j, err
	}
	// The recovery helper lives beside its backup, not in disposable download storage.
	if err = copyExclusive(target, filepath.Join(work, "previous.exe")); err != nil {
		return j, err
	}
	hash, err := fileHash(filepath.Join(work, "previous.exe"))
	if err != nil {
		return j, err
	}
	helperHash, err := fileHash(filepath.Join(work, "FolderBridge-Updater.exe"))
	if err != nil {
		return j, err
	}
	candidateHash, err := fileHash(filepath.Join(work, "FolderBridge.exe"))
	if err != nil {
		return j, err
	}
	j = Journal{Target: target, Work: work, Version: m.Version, PreviousHash: hash, HelperHash: helperHash, CandidateHash: candidateHash, Result: p.Result, Phase: "prepared"}
	if err = WriteJSON(journalPath, j); err != nil {
		return j, err
	}
	ok = true
	return j, nil
}

// Run is called only by the standalone helper after explicit app approval.
func Run(ctx context.Context, p Plan) error {
	release, err := installationLease(p.Target)
	if err != nil {
		return err
	}
	defer release()
	wait, closeWait, err := parentWaiter(p.ParentPID)
	if err != nil {
		return err
	}
	defer closeWait()
	j, err := Prepare(p)
	if err != nil {
		return err
	}
	if err = WriteJSON(filepath.Join(p.Stage, "helper-ready.json"), struct{ Ready bool }{true}); err != nil {
		return err
	}
	if err = wait(ctx); err != nil {
		// No replacement occurred. A timeout must not create a future surprise install.
		os.Remove(filepath.Join(filepath.Dir(j.Target), JournalName))
		os.RemoveAll(j.Work)
		return err
	}
	return installAndMonitor(ctx, j)
}
func installAndMonitor(ctx context.Context, j Journal) error {
	path := filepath.Join(filepath.Dir(j.Target), JournalName)
	j.Phase = "replacing"
	if err := WriteJSON(path, j); err != nil {
		return err
	}
	candidateHash, err := fileHash(filepath.Join(j.Work, "FolderBridge.exe"))
	if err != nil || candidateHash != j.CandidateHash {
		return rollback(j, "candidate integrity changed after preparation")
	}
	if err := renameRetry(filepath.Join(j.Work, "FolderBridge.exe"), j.Target); err != nil {
		return rollback(j, err.Error())
	}
	j.Phase = "testing"
	if err := WriteJSON(path, j); err != nil {
		return rollback(j, err.Error())
	}
	if err := launchHealthy(ctx, j.Target, j.Version); err != nil {
		return rollback(j, err.Error())
	}
	j.Phase = "confirmed"
	if err := WriteJSON(path, j); err != nil {
		return err
	}
	return finish(j)
}
func finish(j Journal) error {
	// Keep the previous version as a durable backup until the next successful update.
	if err := renameRetry(filepath.Join(j.Work, "previous.exe"), filepath.Join(filepath.Dir(j.Target), "FolderBridge.previous.exe")); err != nil {
		return err
	}
	if err := WriteJSON(j.Result, Result{ID: filepath.Base(j.Work), Version: j.Version, Message: "Installed v" + j.Version + "; configuration, API and synchronization initialization healthy"}); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(filepath.Dir(j.Target), JournalName)); err != nil {
		return err
	}
	// Do not remove Work here: on Windows its helper may itself still be executing.
	return nil
}
func rollback(j Journal, reason string) error {
	hash, err := fileHash(filepath.Join(j.Work, "previous.exe"))
	if err != nil || hash != j.PreviousHash {
		return errors.New("backup integrity check failed; preserve update directory and recover manually")
	}
	// Preserve backup even if replacement fails or power is lost during rollback.
	restore := filepath.Join(j.Work, "restore.exe")
	if _, err = os.Lstat(restore); errors.Is(err, os.ErrNotExist) {
		if err = copyExclusive(filepath.Join(j.Work, "previous.exe"), restore); err != nil {
			return err
		}
	}
	restoreHash, err := fileHash(restore)
	if err != nil || restoreHash != j.PreviousHash {
		// A crash while copying restore.exe must never publish a partial backup.
		if err = os.Remove(restore); err != nil {
			return err
		}
		if err = copyExclusive(filepath.Join(j.Work, "previous.exe"), restore); err != nil {
			return err
		}
		restoreHash, err = fileHash(restore)
		if err != nil || restoreHash != j.PreviousHash {
			return errors.New("restore integrity check failed")
		}
	}
	if err = renameRetry(restore, j.Target); err != nil {
		return err
	}
	if err = WriteJSON(j.Result, Result{ID: filepath.Base(j.Work), Version: j.Version, FailedVersion: j.Version, Message: "Rolled back v" + j.Version + ": " + reason}); err != nil {
		return err
	}
	if err = os.Remove(filepath.Join(filepath.Dir(j.Target), JournalName)); err != nil {
		return err
	}
	cmd := exec.Command(j.Target, "--background")
	cmd.Env = cleanHealthEnv()
	if err = cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// Recover is conservative: an interrupted, unconfirmed update is rolled back.
// A confirmed transaction is finalized, never downgraded.
func Recover(ctx context.Context, path string, parent int) error {
	var j Journal
	if err := readJSON(path, &j); err != nil {
		return err
	}
	if err := validateJournal(path, j); err != nil {
		return err
	}
	release, err := installationLease(j.Target)
	if err != nil {
		return err
	}
	defer release()
	manifest, err := VerifyStage(j.Work)
	if err != nil {
		return err
	}
	if manifest.Version != j.Version {
		return errors.New("recovery journal version differs from signed release")
	}
	wait, closeWait, err := parentWaiter(parent)
	if err != nil {
		return err
	}
	defer closeWait()
	if err = wait(ctx); err != nil {
		return err
	}
	if j.Phase == "confirmed" {
		// The confirmation journal is not a signature. Measure the installed target
		// against a fresh extraction of the signed ZIP before relaunching it.
		checkDir, e := os.MkdirTemp(j.Work, "confirmed-check-")
		if e != nil {
			return e
		}
		defer os.RemoveAll(checkDir)
		if e = Extract(filepath.Join(j.Work, "release.zip"), checkDir); e != nil {
			return e
		}
		expected, e := fileHash(filepath.Join(checkDir, "FolderBridge.exe"))
		if e != nil {
			return e
		}
		actual, e := fileHash(j.Target)
		if e != nil || actual != expected {
			return errors.New("installed candidate integrity check failed during confirmed recovery")
		}
		if _, err = os.Stat(filepath.Join(j.Work, "previous.exe")); errors.Is(err, os.ErrNotExist) {
			// finish may have already retained the backup before the interrupted result write.
			hash, e := fileHash(filepath.Join(filepath.Dir(j.Target), "FolderBridge.previous.exe"))
			if e != nil || hash != j.PreviousHash {
				return errors.New("retained backup integrity check failed")
			}
			if err = WriteJSON(j.Result, Result{ID: filepath.Base(j.Work), Version: j.Version, Message: "Update confirmed; recovered interrupted finalization"}); err != nil {
				return err
			}
			if err = os.Remove(path); err != nil {
				return err
			}
		} else if err = finish(j); err != nil {
			return err
		}
		cmd := exec.Command(j.Target, "--background")
		if err = cmd.Start(); err != nil {
			return err
		}
		return cmd.Process.Release()
	}
	return rollback(j, "interrupted installation or startup")
}
func validateJournal(path string, j Journal) error {
	if filepath.Clean(path) != filepath.Join(filepath.Dir(j.Target), JournalName) || filepath.Dir(j.Work) != filepath.Dir(j.Target) || !strings.HasPrefix(filepath.Base(j.Work), ".folderbridge-install-") || filepath.Base(j.Target) != "FolderBridge.exe" {
		return errors.New("invalid recovery journal paths")
	}
	for _, p := range []string{path, j.Work, j.Target} {
		if err := SafePath(p); err != nil {
			return err
		}
	}
	switch j.Phase {
	case "prepared", "replacing", "testing", "confirmed":
	default:
		return errors.New("invalid recovery phase")
	}
	return nil
}

// RecoveryCommand lets an app started after power loss delegate recovery before
// opening the API or touching synchronization. The journal is installation-local.
func RecoveryCommand(target string) (*exec.Cmd, error) {
	path := filepath.Join(filepath.Dir(target), JournalName)
	var j Journal
	if err := readJSON(path, &j); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	if err := validateJournal(path, j); err != nil {
		return nil, err
	}
	helper := filepath.Join(j.Work, "FolderBridge-Updater.exe")
	if err := SafePath(helper); err != nil {
		return nil, err
	}
	hash, err := fileHash(helper)
	if err != nil || hash != j.HelperHash {
		return nil, errors.New("recovery helper integrity check failed; recover manually using preserved backup")
	}
	// The helper was extracted from the signed, verified ZIP during Prepare.
	manifest, err := VerifyStage(j.Work)
	if err != nil {
		return nil, err
	}
	if manifest.Version != j.Version {
		return nil, errors.New("recovery journal does not match signed release")
	}
	launchDir, err := os.MkdirTemp(j.Work, "recovery-launch-")
	if err != nil {
		return nil, err
	}
	if err = Extract(filepath.Join(j.Work, "release.zip"), launchDir); err != nil {
		os.RemoveAll(launchDir)
		return nil, err
	}
	// Execute a fresh extraction from signed evidence, not a journal-selected file.
	return exec.Command(filepath.Join(launchDir, "FolderBridge-Updater.exe"), "--recover", path, "--parent", fmt.Sprint(os.Getpid())), nil
}
func cleanHealthEnv() []string {
	var env []string
	for _, v := range os.Environ() {
		if !strings.HasPrefix(v, HealthEnv+"=") && !strings.HasPrefix(v, TokenEnv+"=") {
			env = append(env, v)
		}
	}
	return env
}
func launchHealthy(ctx context.Context, target, expectedVersion string) error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer ln.Close()
	var nonce [32]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return err
	}
	token := hex.EncodeToString(nonce[:])
	healthy := make(chan struct{}, 1)
	server := &http.Server{ReadHeaderTimeout: 2 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/healthy" || r.Header.Get("X-FolderBridge-Version") != expectedVersion || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			http.Error(w, "unauthorized", 403)
			return
		}
		select {
		case healthy <- struct{}{}:
		default:
		}
		w.WriteHeader(204)
	})}
	go server.Serve(ln)
	defer server.Close()
	cmd := exec.Command(target, "--background")
	cmd.Env = append(cleanHealthEnv(), HealthEnv+"=http://"+ln.Addr().String()+"/healthy", TokenEnv+"="+token)
	if err = cmd.Start(); err != nil {
		return fmt.Errorf("new application did not start: %w", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	select {
	case <-healthy:
		return nil
	case err = <-exited:
		return fmt.Errorf("new application exited before health confirmation: %v", err)
	case <-ctx.Done():
		err = ctx.Err()
	case <-timer.C:
		err = errors.New("new application health check timed out after 30 seconds")
	}
	// Only the process started by this helper is terminated, never another instance.
	if killErr := cmd.Process.Kill(); killErr != nil {
		select {
		case <-exited:
			return err
		default:
			return fmt.Errorf("cannot stop failed update; backup retained: %w", killErr)
		}
	}
	<-exited
	return err
}

// ReportHealthy is called only after config load, API startup and successful
// engine initialization. Never follows redirects or uses a corporate proxy for
// its loopback-only authenticated handshake.
func ReportHealthy(ctx context.Context) error {
	raw, token := os.Getenv(HealthEnv), os.Getenv(TokenEnv)
	os.Unsetenv(HealthEnv)
	os.Unsetenv(TokenEnv)
	if raw == "" && token == "" {
		return nil
	}
	if len(token) != 64 {
		return errors.New("invalid update health token")
	}
	u, err := urlForHealth(raw)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-FolderBridge-Version", Version)
	c := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("health redirects forbidden") }}
	res, err := c.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 204 {
		return errors.New("update health confirmation refused")
	}
	return nil
}
