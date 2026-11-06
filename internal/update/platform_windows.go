//go:build windows

package update

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// SafePath rejects reparse points in every existing ancestor. Updates must not
// follow junctions/symlinks into a different installation or volume.
func SafePath(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	for {
		p, err := syscall.UTF16PtrFromString(abs)
		if err != nil {
			return err
		}
		attr, err := syscall.GetFileAttributes(p)
		if err != nil {
			if !errors.Is(err, syscall.ERROR_FILE_NOT_FOUND) && !errors.Is(err, syscall.ERROR_PATH_NOT_FOUND) {
				return err
			}
		} else if attr&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			return fmt.Errorf("update path contains a Windows reparse point: %s", abs)
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return nil
		}
		abs = parent
	}
}

// A non-owned named mutex is a process-lifetime installation lease. The first
// open creates it; subsequent opens must not proceed while that handle exists.
func installationLease(target string) (func(), error) {
	abs, err := filepath.Abs(target)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256([]byte(strings.ToLower(abs)))
	name, err := syscall.UTF16PtrFromString(fmt.Sprintf(`Local\FolderBridgeUpdater-%x`, hash[:16]))
	if err != nil {
		return nil, err
	}
	create := syscall.NewLazyDLL("kernel32.dll").NewProc("CreateMutexW")
	h, _, callErr := create.Call(0, 0, uintptr(unsafe.Pointer(name)))
	if h == 0 {
		return nil, callErr
	}
	if callErr == syscall.ERROR_ALREADY_EXISTS {
		syscall.CloseHandle(syscall.Handle(h))
		return nil, errors.New("another updater is already installing or recovering this application")
	}
	return func() { syscall.CloseHandle(syscall.Handle(h)) }, nil
}
func parentWaiter(pid int) (func(context.Context) error, func(), error) {
	if pid <= 0 || pid == os.Getpid() {
		return nil, nil, errors.New("invalid parent process")
	}
	h, err := syscall.OpenProcess(syscall.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return nil, nil, fmt.Errorf("open application process for graceful exit: %w", err)
	}
	return func(ctx context.Context) error {
		for {
			result, err := syscall.WaitForSingleObject(h, 100)
			if err != nil {
				return err
			}
			if result == syscall.WAIT_OBJECT_0 {
				return nil
			}
			if result != uint32(syscall.WAIT_TIMEOUT) {
				return errors.New("unexpected process wait result")
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Millisecond):
			}
		}
	}, func() { syscall.CloseHandle(h) }, nil
}
