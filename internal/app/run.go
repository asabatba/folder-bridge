package app

import (
	"context"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"time"
)

const uiAddress = "127.0.0.1:47831"

// Version is build metadata, optionally set by the release workflow with -X.
var Version = "4.0.0-dev"

func openBrowser() {
	if runtime.GOOS == "windows" {
		exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", "http://"+uiAddress+"/").Start()
	}
}

// Run starts FolderBridge and owns its process-level startup and shutdown.
func Run() {
	background := flag.Bool("background", false, "start minimized in the system tray")
	flag.Parse()
	home, err := configHome()
	if err != nil {
		log.Fatal(err)
	}
	if err = os.MkdirAll(home, 0700); err != nil {
		log.Fatal(err)
	}
	app, err := newApp(home)
	if err != nil {
		log.Fatal(err)
	}
	listener, err := net.Listen("tcp", uiAddress)
	if err != nil {
		openBrowser()
		return
	}
	uiServer := &http.Server{Handler: app.uiHandler(), ReadHeaderTimeout: 10 * time.Second}
	go uiServer.Serve(listener)
	stopTray := startTray(app)
	if err := app.startPeer(); err != nil {
		app.setError(err.Error())
	}
	if !*background {
		openBrowser()
	}
	<-app.quit
	stopTray()
	app.stopPeer()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	uiServer.Shutdown(ctx)
}
