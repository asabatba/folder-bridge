//go:build windows && amd64

package main

import "unsafe"

// Compile-time ABI checks for the native Win32 data structures used by the tray.
var (
	_ [80 - int(unsafe.Sizeof(trayWindowClass{}))]byte
	_ [int(unsafe.Sizeof(trayWindowClass{})) - 80]byte
	_ [48 - int(unsafe.Sizeof(trayMsg{}))]byte
	_ [int(unsafe.Sizeof(trayMsg{})) - 48]byte
	_ [976 - int(unsafe.Sizeof(notifyIconData{}))]byte
	_ [int(unsafe.Sizeof(notifyIconData{})) - 976]byte
)
