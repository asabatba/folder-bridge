//go:build windows

package update

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

func buildFixture(t *testing.T, dir, mode string) string {
	t.Helper()
	path := filepath.Join(dir, mode+".exe")
	cmd := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go.exe"), "build", "-trimpath", "-ldflags", "-X main.mode="+mode+" -X folderbridge/internal/update.Version=3.4.0", "-o", path, "./testdata/health-app")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build native fixture: %v\n%s", err, out)
	}
	return path
}
func stageFixture(t *testing.T, key ed25519.PrivateKey, app, helper string) string {
	t.Helper()
	stage := t.TempDir()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for name, src := range map[string]string{"FolderBridge.exe": app, "FolderBridge-Updater.exe": helper} {
		f, err := os.Open(src)
		if err != nil {
			t.Fatal(err)
		}
		w, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, err = io.Copy(w, f)
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	m := testManifest("3.4.0", b.Bytes())
	data, _ := json.Marshal(m)
	for name, data := range map[string][]byte{"release.zip": b.Bytes(), "manifest.json": data, "manifest.sig": ed25519.Sign(key, data)} {
		if err := os.WriteFile(filepath.Join(stage, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return stage
}
func waitFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}
func cleanupCandidate(t *testing.T, dir string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "candidate-pid"))
	if err != nil {
		return
	}
	pid, err := strconv.Atoi(string(b))
	if err != nil {
		t.Fatal(err)
	}
	// Keep one process handle across identity verification and termination. A
	// fixture's PID may have been reused after a failed candidate already exited;
	// never reopen by PID and accidentally terminate an unrelated process.
	h, err := syscall.OpenProcess(syscall.PROCESS_QUERY_INFORMATION|syscall.PROCESS_TERMINATE|syscall.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return
	}
	defer syscall.CloseHandle(h)
	var image [32768]uint16
	size := uint32(len(image))
	query := syscall.NewLazyDLL("kernel32.dll").NewProc("QueryFullProcessImageNameW")
	ok, _, queryErr := query.Call(uintptr(h), 0, uintptr(unsafe.Pointer(&image[0])), uintptr(unsafe.Pointer(&size)))
	if ok == 0 {
		t.Errorf("cannot verify fixture process identity: %v", queryErr)
		return
	}
	if !strings.EqualFold(filepath.Clean(syscall.UTF16ToString(image[:size])), filepath.Join(dir, "FolderBridge.exe")) {
		return
	}
	syscall.TerminateProcess(h, 0)
	if result, err := syscall.WaitForSingleObject(h, 5000); err != nil || result != syscall.WAIT_OBJECT_0 {
		t.Errorf("fixture did not exit: %d %v", result, err)
	}
	time.Sleep(100 * time.Millisecond)
}
func TestWindowsUpdateIntegration(t *testing.T) {
	_, key := testKey(t)
	bins := t.TempDir()
	old := buildFixture(t, bins, "old")
	healthy := buildFixture(t, bins, "healthy")
	failed := buildFixture(t, bins, "fail")
	unauthenticated := buildFixture(t, bins, "unauthenticated")
	wrongVersion := buildFixture(t, bins, "wrong-version")
	hang := buildFixture(t, bins, "hang")
	helper := filepath.Join(bins, "FolderBridge-Updater.exe")
	cmd := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go.exe"), "build", "-trimpath", "-ldflags", "-X folderbridge/internal/update.PublicKey="+PublicKey, "-o", helper, "../../cmd/folderbridge-updater")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build native helper: %v\n%s", err, out)
	}
	newPlan := func(t *testing.T, candidate string) Plan {
		t.Helper()
		dir := t.TempDir()
		target := filepath.Join(dir, "FolderBridge.exe")
		if err := copyExclusive(old, target); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { cleanupCandidate(t, dir) })
		return Plan{Stage: stageFixture(t, key, candidate, helper), Target: target, CurrentVersion: "3.3.0", Result: filepath.Join(dir, "update-result.json")}
	}
	t.Run("fixture cleanup cannot terminate a different process image", func(t *testing.T) {
		dir := t.TempDir()
		parent := exec.Command(old, "--hold")
		if err := parent.Start(); err != nil {
			t.Fatal(err)
		}
		defer func() { parent.Process.Kill(); parent.Wait() }()
		if err := os.WriteFile(filepath.Join(dir, "candidate-pid"), []byte(strconv.Itoa(parent.Process.Pid)), 0600); err != nil {
			t.Fatal(err)
		}
		h, err := syscall.OpenProcess(syscall.SYNCHRONIZE, false, uint32(parent.Process.Pid))
		if err != nil {
			t.Fatal(err)
		}
		defer syscall.CloseHandle(h)
		cleanupCandidate(t, dir)
		if result, err := syscall.WaitForSingleObject(h, 100); err != nil || result != uint32(syscall.WAIT_TIMEOUT) {
			t.Fatalf("cleanup terminated an unrelated image: %d %v", result, err)
		}
	})
	t.Run("standalone helper waits for actual running app then commits healthy update", func(t *testing.T) {
		p := newPlan(t, healthy)
		parent := exec.Command(p.Target, "--hold")
		if err := parent.Start(); err != nil {
			t.Fatal(err)
		}
		defer func() { parent.Process.Kill(); parent.Wait() }()
		p.ParentPID = parent.Process.Pid
		planPath := filepath.Join(p.Stage, "install-plan.json")
		if err := WriteJSON(planPath, p); err != nil {
			t.Fatal(err)
		}
		child := exec.Command(helper, "--plan", planPath)
		var out bytes.Buffer
		child.Stdout = &out
		child.Stderr = &out
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		defer child.Process.Kill()
		waitFile(t, filepath.Join(p.Stage, "helper-ready.json"))
		got, _ := fileHash(p.Target)
		want, _ := fileHash(old)
		if got != want {
			t.Fatal("helper replaced still-running application")
		}
		parent.Process.Kill()
		parent.Wait()
		done := make(chan error, 1)
		go func() { done <- child.Wait() }()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("helper failed: %v %s", err, out.String())
			}
		case <-time.After(20 * time.Second):
			t.Fatalf("helper timed out: %s", out.String())
		}
		got, _ = fileHash(p.Target)
		want, _ = fileHash(healthy)
		if got != want {
			t.Fatal("new executable not installed")
		}
		got, _ = fileHash(filepath.Join(filepath.Dir(p.Target), "FolderBridge.previous.exe"))
		want, _ = fileHash(old)
		if got != want {
			t.Fatal("previous version backup not retained")
		}
		var result Result
		if err := readJSON(p.Result, &result); err != nil {
			t.Fatal(err)
		}
		if result.FailedVersion != "" {
			t.Fatal("healthy update quarantined")
		}
		if _, err := os.Stat(filepath.Join(filepath.Dir(p.Target), JournalName)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("confirmed journal retained", err)
		}
	})
	for _, tc := range []struct{ name, app string }{{"failed initialization rolls back", failed}, {"unauthenticated health cannot confirm", unauthenticated}, {"candidate older than signed version rolls back", wrongVersion}} {
		t.Run(tc.name, func(t *testing.T) {
			p := newPlan(t, tc.app)
			j, err := Prepare(p)
			if err != nil {
				t.Fatal(err)
			}
			if err = installAndMonitor(context.Background(), j); err != nil {
				t.Fatal(err)
			}
			waitFile(t, filepath.Join(filepath.Dir(p.Target), "rollback-launched"))
			got, _ := fileHash(p.Target)
			want, _ := fileHash(old)
			if got != want {
				t.Fatal("old executable not restored")
			}
			var result Result
			if err = readJSON(p.Result, &result); err != nil {
				t.Fatal(err)
			}
			if result.FailedVersion != "3.4.0" {
				t.Fatal("failed version not quarantined")
			}
		})
	}
	t.Run("health timeout terminates only candidate", func(t *testing.T) {
		p := newPlan(t, hang)
		j, err := Prepare(p)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
		defer cancel()
		if err = installAndMonitor(ctx, j); err != nil {
			t.Fatal(err)
		}
		waitFile(t, filepath.Join(filepath.Dir(p.Target), "rollback-launched"))
	})
	t.Run("interrupted replacement and partial restore recover", func(t *testing.T) {
		for _, phase := range []string{"prepared", "replacing", "testing"} {
			t.Run(phase, func(t *testing.T) {
				p := newPlan(t, failed)
				j, err := Prepare(p)
				if err != nil {
					t.Fatal(err)
				}
				j.Phase = phase
				path := filepath.Join(filepath.Dir(p.Target), JournalName)
				if err = WriteJSON(path, j); err != nil {
					t.Fatal(err)
				}
				if phase != "prepared" {
					if err = os.Rename(filepath.Join(j.Work, "FolderBridge.exe"), p.Target); err != nil {
						t.Fatal(err)
					}
				}
				os.WriteFile(filepath.Join(j.Work, "restore.exe"), []byte("interrupted partial backup copy"), 0600)
				parent := exec.Command(old, "--hold")
				if err = parent.Start(); err != nil {
					t.Fatal(err)
				}
				parent.Process.Kill() // Retain its process handle so the PID remains waitable.
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				err = Recover(ctx, path, parent.Process.Pid)
				parent.Wait()
				if err != nil {
					t.Fatal(err)
				}
				waitFile(t, filepath.Join(filepath.Dir(p.Target), "rollback-launched"))
				got, _ := fileHash(p.Target)
				want, _ := fileHash(old)
				if got != want {
					t.Fatal("interrupted transaction lost backup")
				}
			})
		}
	})
	t.Run("confirmed recovery finalizes without downgrade", func(t *testing.T) {
		for _, backupMoved := range []bool{false, true} {
			name := "before backup retention"
			if backupMoved {
				name = "after backup retention"
			}
			t.Run(name, func(t *testing.T) {
				p := newPlan(t, healthy)
				j, err := Prepare(p)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.Rename(filepath.Join(j.Work, "FolderBridge.exe"), p.Target); err != nil {
					t.Fatal(err)
				}
				if backupMoved {
					if err = os.Rename(filepath.Join(j.Work, "previous.exe"), filepath.Join(filepath.Dir(p.Target), "FolderBridge.previous.exe")); err != nil {
						t.Fatal(err)
					}
				}
				j.Phase = "confirmed"
				path := filepath.Join(filepath.Dir(p.Target), JournalName)
				if err = WriteJSON(path, j); err != nil {
					t.Fatal(err)
				}
				parent := exec.Command(old, "--hold")
				if err = parent.Start(); err != nil {
					t.Fatal(err)
				}
				parent.Process.Kill()
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				err = Recover(ctx, path, parent.Process.Pid)
				parent.Wait()
				if err != nil {
					t.Fatal(err)
				}
				waitFile(t, filepath.Join(filepath.Dir(p.Target), "candidate-pid"))
				got, _ := fileHash(p.Target)
				want, _ := fileHash(healthy)
				if got != want {
					t.Fatal("confirmed candidate was downgraded")
				}
				var result Result
				if err = readJSON(p.Result, &result); err != nil || result.FailedVersion != "" {
					t.Fatal("confirmed transaction quarantined", err)
				}
			})
		}
	})
	t.Run("confirmed recovery rejects modified installed candidate", func(t *testing.T) {
		p := newPlan(t, healthy)
		j, err := Prepare(p)
		if err != nil {
			t.Fatal(err)
		}
		j.Phase = "confirmed"
		path := filepath.Join(filepath.Dir(p.Target), JournalName)
		if err = WriteJSON(path, j); err != nil {
			t.Fatal(err)
		}
		parent := exec.Command(old, "--hold")
		if err = parent.Start(); err != nil {
			t.Fatal(err)
		}
		parent.Process.Kill()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err = Recover(ctx, path, parent.Process.Pid)
		parent.Wait()
		if err == nil || !bytes.Contains([]byte(err.Error()), []byte("candidate integrity")) {
			t.Fatalf("unverified installed executable accepted: %v", err)
		}
	})
	t.Run("locked executable preserves live file and backup", func(t *testing.T) {
		p := newPlan(t, failed)
		j, err := Prepare(p)
		if err != nil {
			t.Fatal(err)
		}
		name, _ := syscall.UTF16PtrFromString(p.Target)
		h, err := syscall.CreateFile(name, syscall.GENERIC_READ, syscall.FILE_SHARE_READ, nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
		if err != nil {
			t.Fatal(err)
		}
		err = installAndMonitor(context.Background(), j)
		syscall.CloseHandle(h)
		if err == nil {
			t.Fatal("locked destination unexpectedly replaced")
		}
		got, _ := fileHash(p.Target)
		want, _ := fileHash(old)
		if got != want {
			t.Fatal("locked executable changed")
		}
		got, _ = fileHash(filepath.Join(j.Work, "previous.exe"))
		if got != want {
			t.Fatal("backup lost on sharing violation")
		}
	})
	t.Run("recovery helper tampering rejected before execution", func(t *testing.T) {
		p := newPlan(t, healthy)
		j, err := Prepare(p)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = RecoveryCommand(p.Target); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(j.Work, "FolderBridge-Updater.exe"), []byte("tampered"), 0700)
		if _, err = RecoveryCommand(p.Target); err == nil {
			t.Fatal("tampered recovery helper accepted")
		}
	})
	t.Run("installation lease excludes concurrent recovery", func(t *testing.T) {
		p := newPlan(t, healthy)
		release, err := installationLease(p.Target)
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		other, err := installationLease(p.Target)
		if err == nil {
			other()
			t.Fatal("concurrent updater lease accepted")
		}
	})
	t.Run("read-only application directory reports manual upgrade", func(t *testing.T) {
		p := newPlan(t, healthy)
		// DENY write-data/add-file to the current user on a disposable directory.
		// icacls is native and changes no installed app or machine-wide policy.
		dir := filepath.Dir(p.Target)
		user := os.Getenv("USERDOMAIN") + `\` + os.Getenv("USERNAME")
		deny := exec.Command("icacls.exe", dir, "/deny", user+":(WD)")
		if out, err := deny.CombinedOutput(); err != nil {
			t.Fatalf("set disposable ACL: %v %s", err, out)
		}
		defer func() {
			out, err := exec.Command("icacls.exe", dir, "/remove:d", user).CombinedOutput()
			if err != nil {
				t.Errorf("restore disposable ACL: %v %s", err, out)
			}
		}()
		if _, err := Prepare(p); err == nil {
			t.Fatal("unwritable installation accepted")
		} else if !bytes.Contains([]byte(err.Error()), []byte("manually upgrade")) {
			t.Fatalf("missing manual guidance: %v", err)
		}
	})
}
func TestWindowsSafePathRejectsJunction(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	os.Mkdir(real, 0700)
	link := filepath.Join(root, "alias")
	if out, err := exec.Command("cmd.exe", "/c", "mklink", "/J", link, real).CombinedOutput(); err != nil {
		t.Fatalf("junction setup: %v %s", err, out)
	}
	t.Cleanup(func() {
		if err := os.Remove(link); err != nil && !os.IsNotExist(err) {
			t.Error(err)
		}
	})
	if err := SafePath(filepath.Join(link, "FolderBridge.exe")); err == nil {
		t.Fatal("junction ancestor accepted")
	}
}
