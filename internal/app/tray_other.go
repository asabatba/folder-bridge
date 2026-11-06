//go:build !windows

package app

func startTray(app *application) func() { return func() {} }
