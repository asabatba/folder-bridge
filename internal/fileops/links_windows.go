//go:build windows

package fileops

import (
	"os"
	"syscall"
)

func isPathLink(info os.FileInfo) bool {
	if info.Mode()&os.ModeSymlink != 0 {
		return true
	}
	// Include junctions and other reparse tags, even if Go does not expose
	// the particular tag as ModeSymlink.
	data, ok := info.Sys().(*syscall.Win32FileAttributeData)
	return ok && data.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0
}
