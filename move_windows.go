//go:build windows

package main

import (
	"fmt"
	"syscall"
	"unsafe"
)

var moveFileProc = syscall.NewLazyDLL("kernel32.dll").NewProc("MoveFileW")

// MoveFileW fails if the destination already exists (unlike MoveFileEx with
// REPLACE_EXISTING), and a same-volume rename does not copy file contents.
func moveFileNoReplace(source, dest string) error {
	from, err := syscall.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	to, err := syscall.UTF16PtrFromString(dest)
	if err != nil {
		return err
	}
	ok, _, callErr := moveFileProc.Call(uintptr(unsafe.Pointer(from)), uintptr(unsafe.Pointer(to)))
	if ok == 0 {
		return fmt.Errorf("MoveFileW: %w", callErr)
	}
	return nil
}
