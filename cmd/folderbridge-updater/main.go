// FolderBridge-Updater performs only installation and recovery. It has no
// synchronization engine, UI, elevation path, or release-discovery capability.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"folderbridge/internal/update"
)

func main() {
	planPath := flag.String("plan", "", "approved installation plan")
	recoverPath := flag.String("recover", "", "recover interrupted installation journal")
	parent := flag.Int("parent", 0, "process to wait for before recovery")
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	var err error
	if *recoverPath != "" && *planPath == "" {
		err = update.Recover(ctx, *recoverPath, *parent)
	} else if *planPath != "" && *recoverPath == "" {
		var p update.Plan
		f, e := os.Open(*planPath)
		err = e
		if err == nil {
			err = json.NewDecoder(io.LimitReader(f, 64<<10)).Decode(&p)
			f.Close()
		}
		if err == nil {
			err = update.Run(ctx, p)
		}
		if err != nil {
			os.WriteFile(filepath.Join(filepath.Dir(*planPath), "helper-error.txt"), []byte(err.Error()), 0600)
		}
	} else {
		err = fmt.Errorf("supply exactly one of --plan or --recover")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
