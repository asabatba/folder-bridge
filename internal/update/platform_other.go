//go:build !windows

package update

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

func SafePath(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	for {
		st, err := os.Lstat(abs)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return err
			}
		} else if st.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("update path contains a symlink: %s", abs)
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return nil
		}
		abs = parent
	}
}
func installationLease(_ string) (func(), error) {
	return nil, errors.New("update installation is supported only on Windows")
}
func parentWaiter(_ int) (func(context.Context) error, func(), error) {
	return nil, nil, errors.New("update installation is supported only on Windows")
}
