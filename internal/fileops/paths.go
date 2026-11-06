package fileops

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// RejectPathLinks rejects links in a path and its ancestors without changing its
// identity. Missing suffixes are allowed for validation before directory creation.
func RejectPathLinks(name string) error {
	if name == "" {
		return errors.New("choose a folder first")
	}
	full, err := filepath.Abs(name)
	if err != nil {
		return err
	}
	for current := full; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err == nil && isPathLink(info) {
			return fmt.Errorf("symbolic links, junctions and reparse points are not allowed in sync paths: %q; select the real directory", current)
		}
		if filepath.Dir(current) == current {
			break
		}
	}
	return nil
}

// CheckSyncRoot treats missing, linked and unsupported roots as errors, never an
// empty successful scan (which could otherwise be interpreted as deletion).
func CheckSyncRoot(root string) error {
	if err := RejectPathLinks(root); err != nil {
		return err
	}
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("sync root is not a directory")
	}
	return nil
}

// CheckedPath validates a canonical protocol-relative path inside a real root,
// rejecting links/junctions in existing descendants as well as root ancestors.
func CheckedPath(root, relative string) (string, error) {
	if err := CheckSyncRoot(root); err != nil {
		return "", err
	}
	if relative == "" || strings.Contains(relative, "\\") || strings.HasPrefix(relative, "/") || path.Clean(relative) != relative {
		return "", errors.New("invalid relative path")
	}
	native := filepath.FromSlash(relative)
	if !filepath.IsLocal(native) {
		return "", errors.New("path is not local")
	}
	dest := filepath.Join(root, native)
	back, err := filepath.Rel(root, dest)
	if err != nil || back == ".." || strings.HasPrefix(back, ".."+string(os.PathSeparator)) {
		return "", errors.New("path escapes shared directory")
	}
	segment := root
	for _, part := range strings.Split(native, string(os.PathSeparator)) {
		segment = filepath.Join(segment, part)
		info, err := os.Lstat(segment)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		if isPathLink(info) {
			return "", errors.New("symbolic links, junctions and reparse points are not allowed")
		}
	}
	return dest, nil
}
