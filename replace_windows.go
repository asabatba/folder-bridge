//go:build windows

package main

import (
	"errors"
	"syscall"
)

// Windows ERROR_SHARING_VIOLATION and ERROR_LOCK_VIOLATION are temporary when
// antivirus, indexers or applications have the destination open exclusively.
func retryableReplaceError(err error) bool {
	return errors.Is(err, syscall.Errno(32)) || errors.Is(err, syscall.Errno(33))
}
