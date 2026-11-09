//go:build windows

package app

import (
	_ "embed"
	"encoding/binary"
	"fmt"
	"runtime"
	"strings"
	"unsafe"
)

// The original user-supplied artwork is retained alongside these multi-size ICOs.
// Only the three ready-to-use icons are embedded; no external asset is needed.
var (
	//go:embed icons/tray-normal.ico
	trayNormalICO []byte
	//go:embed icons/tray-paused.ico
	trayPausedICO []byte
	//go:embed icons/tray-error.ico
	trayErrorICO []byte

	procCreateIconFromResourceEx = trayUser32.NewProc("CreateIconFromResourceEx")
	procDestroyIcon              = trayUser32.NewProc("DestroyIcon")
	procGetSystemMetrics         = trayUser32.NewProc("GetSystemMetrics")
)

const (
	trayNormal = iota
	trayPaused
	trayError
)

type trayIcons [3]uintptr

func trayIconVariant(state string) int {
	if state == "Paused" {
		return trayPaused
	}
	if strings.Contains(state, "Error") || strings.Contains(state, "Partial") {
		return trayError
	}
	return trayNormal
}

// Select the smallest available frame at least as large as the requested size,
// or the largest smaller frame. Windows scales the selected DIB to the exact size.
func trayIconFrame(ico []byte, size int) ([]byte, error) {
	if size < 1 || len(ico) < 6 || binary.LittleEndian.Uint16(ico[:2]) != 0 || binary.LittleEndian.Uint16(ico[2:4]) != 1 {
		return nil, fmt.Errorf("invalid tray ICO header or size")
	}
	count := int(binary.LittleEndian.Uint16(ico[4:6]))
	if count == 0 || len(ico) < 6+16*count {
		return nil, fmt.Errorf("invalid tray ICO directory")
	}
	best, bestWidth := -1, 0
	for i := 0; i < count; i++ {
		entry := ico[6+16*i : 6+16*(i+1)]
		width, height := int(entry[0]), int(entry[1])
		if width == 0 {
			width = 256
		}
		if height == 0 {
			height = 256
		}
		if width != height {
			return nil, fmt.Errorf("tray ICO frame must be square")
		}
		if best < 0 || (width >= size && (bestWidth < size || width < bestWidth)) || (width < size && bestWidth < size && width > bestWidth) {
			best, bestWidth = i, width
		}
	}
	entry := ico[6+16*best : 6+16*(best+1)]
	length := uint64(binary.LittleEndian.Uint32(entry[8:12]))
	offset := uint64(binary.LittleEndian.Uint32(entry[12:16]))
	if length == 0 || offset < uint64(6+16*count) || offset+length > uint64(len(ico)) {
		return nil, fmt.Errorf("invalid tray ICO frame bounds")
	}
	return ico[offset : offset+length], nil
}

func loadTrayIcons(size int) (trayIcons, error) {
	var icons trayIcons
	for i, ico := range [][]byte{trayNormalICO, trayPausedICO, trayErrorICO} {
		frame, err := trayIconFrame(ico, size)
		if err != nil {
			icons.close()
			return trayIcons{}, err
		}
		handle, _, callErr := procCreateIconFromResourceEx.Call(uintptr(unsafe.Pointer(&frame[0])), uintptr(len(frame)), 1, 0x00030000, uintptr(size), uintptr(size), 0)
		runtime.KeepAlive(frame)
		if handle == 0 {
			icons.close()
			return trayIcons{}, fmt.Errorf("create tray icon %d: %v", i, callErr)
		}
		icons[i] = handle
	}
	return icons, nil
}

// These are private, non-shared HICONs, owned by the tray's locked OS thread.
// Never destroy a handle while Shell_NotifyIcon still refers to it.
func (icons *trayIcons) close() {
	for i, handle := range icons {
		if handle != 0 {
			procDestroyIcon.Call(handle)
			icons[i] = 0
		}
	}
}
