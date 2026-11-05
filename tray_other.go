//go:build !windows

package main

func startTray(app *App) func() { return func() {} }
