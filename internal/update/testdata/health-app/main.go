// This small real Windows executable exercises process waits, executable locks,
// authenticated health and rollback without touching the installed application.
package main

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"folderbridge/internal/update"
)

var mode = "fail"

func main() {
	exe, err := os.Executable()
	if err != nil {
		os.Exit(10)
	}
	dir := filepath.Dir(exe)
	if len(os.Args) > 1 && os.Args[1] == "--hold" {
		for {
			time.Sleep(time.Second)
		}
	}
	switch mode {
	case "old":
		os.WriteFile(filepath.Join(dir, "rollback-launched"), []byte("old"), 0600)
	case "healthy", "wrong-version":
		if mode == "wrong-version" {
			update.Version = "3.2.0"
		}
		os.WriteFile(filepath.Join(dir, "candidate-pid"), []byte(strconv.Itoa(os.Getpid())), 0600)
		if err = update.ReportHealthy(context.Background()); err != nil {
			os.Exit(11)
		}
		for {
			time.Sleep(time.Second)
		}
	case "unauthenticated":
		// A wrong token cannot make a candidate healthy.
		os.Setenv(update.TokenEnv, "0000000000000000000000000000000000000000000000000000000000000000")
		if update.ReportHealthy(context.Background()) == nil {
			os.Exit(12)
		}
		os.Exit(13)
	case "hang":
		os.WriteFile(filepath.Join(dir, "candidate-pid"), []byte(strconv.Itoa(os.Getpid())), 0600)
		for {
			time.Sleep(time.Second)
		}
	default:
		os.Exit(42)
	}
}
