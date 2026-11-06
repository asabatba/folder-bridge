//go:build !windows

package fileops

import "os"

func isPathLink(info os.FileInfo) bool {
	return info.Mode()&os.ModeSymlink != 0
}
