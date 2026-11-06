//go:build windows

package syncengine

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"syscall"
	"unsafe"
)

const (
	fileListDirectory   = 0x0001
	flagBackupSemantics = 0x02000000
	notifyFileName      = 0x00000001
	notifyDirName       = 0x00000002
	notifyAttributes    = 0x00000004
	notifySize          = 0x00000008
	notifyLastWrite     = 0x00000010
	notifyCreation      = 0x00000040
)

var readDirectoryChangesW = syscall.NewLazyDLL("kernel32.dll").NewProc("ReadDirectoryChangesW")
var cancelIoEx = syscall.NewLazyDLL("kernel32.dll").NewProc("CancelIoEx")

// watchFolder watches the entire subtree with the native Windows directory
// change API. If the buffer overflows, the empty path forces a full rehash.
// All notifications are advisory: a periodic reconciliation remains enabled.
func watchFolder(ctx context.Context, root string, changed func(string)) error {
	name, err := syscall.UTF16PtrFromString(root)
	if err != nil {
		return err
	}
	h, err := syscall.CreateFile(name, fileListDirectory,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil, syscall.OPEN_EXISTING, flagBackupSemantics, 0)
	if err != nil {
		return fmt.Errorf("open directory watcher: %w", err)
	}
	stopped := make(chan struct{})
	var closeOnce sync.Once
	closeWatcher := func() { closeOnce.Do(func() { _ = syscall.CloseHandle(h) }) }
	go func() {
		select {
		case <-ctx.Done():
			cancelIoEx.Call(uintptr(h), 0)
			closeWatcher() // unblocks synchronous ReadDirectoryChangesW
		case <-stopped:
		}
	}()
	defer close(stopped)
	defer closeWatcher()

	buf := make([]byte, 64<<10)
	for ctx.Err() == nil {
		var n uint32
		ok, _, callErr := readDirectoryChangesW.Call(
			uintptr(h), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 1,
			notifyFileName|notifyDirName|notifyAttributes|notifySize|notifyLastWrite|notifyCreation,
			uintptr(unsafe.Pointer(&n)), 0, 0,
		)
		if ctx.Err() != nil {
			return nil
		}
		if ok == 0 || n == 0 {
			changed("") // buffer overflow or uncertain changes: invalidate all
			if ok == 0 {
				if callErr != syscall.Errno(0) {
					return fmt.Errorf("ReadDirectoryChangesW: %w", callErr)
				}
				return errors.New("ReadDirectoryChangesW failed")
			}
			continue
		}
		if err := parseDirectoryNotifications(buf[:n], changed); err != nil {
			changed("")
		}
	}
	return nil
}

// FILE_NOTIFY_INFORMATION is a linked list of variable-length UTF-16 names.
func parseDirectoryNotifications(data []byte, changed func(string)) error {
	for offset := 0; offset < len(data); {
		if len(data)-offset < 12 {
			return errors.New("short directory notification")
		}
		next := int(binary.LittleEndian.Uint32(data[offset:]))
		size := int(binary.LittleEndian.Uint32(data[offset+8:]))
		if size < 0 || size%2 != 0 || size > len(data)-offset-12 {
			return errors.New("invalid directory notification length")
		}
		chars := make([]uint16, size/2)
		for i := range chars {
			chars[i] = binary.LittleEndian.Uint16(data[offset+12+2*i:])
		}
		name := filepath.ToSlash(syscall.UTF16ToString(chars))
		if name != "" {
			changed(name)
		}
		if next == 0 {
			return nil
		}
		if next < 12 || next > len(data)-offset {
			return errors.New("invalid directory notification offset")
		}
		offset += next
	}
	return nil
}
