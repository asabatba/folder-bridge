package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// rejectPathLinks checks the selected path AND its ancestors. Missing suffixes
// are allowed so folder selection can validate before creating a new directory.
// Never resolve links here: the configured pathname and baseline must retain
// their identity rather than silently following a different directory.
func rejectPathLinks(name string) error {
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

// A missing or unsupported root is an error, never a successful empty scan.
func checkSyncRoot(root string) error {
	if err := rejectPathLinks(root); err != nil {
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
