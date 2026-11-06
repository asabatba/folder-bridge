//go:build !windows

package fileops

import "os"

// Hard-link with O_EXCL-like semantics: Link fails if the destination exists.
// Both paths must be on the same filesystem. An unlink failure rolls back.
func moveFileNoReplace(source, dest string) error {
	if err := os.Link(source, dest); err != nil {
		return err
	}
	if err := os.Remove(source); err != nil {
		_ = os.Remove(dest)
		return err
	}
	return nil
}
