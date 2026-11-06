//go:build !windows

package app

import "errors"

func autostartEnabled() bool { return false }
func setAutostart(enabled bool) error {
	return errors.New("launch at login is only supported on Windows")
}
