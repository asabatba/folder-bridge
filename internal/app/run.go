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

	"folderbridge/internal/update"
)

const uiAddress = "127.0.0.1:47831"

func openBrowser() {
	if runtime.GOOS == "windows" {
		exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", "http://"+uiAddress+"/").Start()
	}
}

// Run starts FolderBridge and owns its process-level startup and shutdown.
func Run() {
	background := flag.Bool("background", false, "start minimized in the system tray")
	flag.Parse()
	// A helper-started candidate must report health, not recursively recover its
	// own in-progress transaction. Ordinary startup recovers before touching sync.
	if runtime.GOOS == "windows" && os.Getenv(update.HealthEnv) == "" {
		exe, err := os.Executable()
		if err != nil {
			log.Fatal(err)
		}
		cmd, err := update.RecoveryCommand(exe)
		if err != nil {
			log.Fatal("update recovery: ", err)
		}
		if cmd != nil {
			if err = cmd.Start(); err != nil {
				log.Fatal("start update recovery: ", err)
			}
			return
		}
	}
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
		// Port occupation never counts as successful candidate health.
		if os.Getenv(update.HealthEnv) != "" {
			log.Fatal("updated application cannot start local API: ", err)
		}
		openBrowser()
		return
	}
	uiServer := &http.Server{Handler: app.uiHandler(), ReadHeaderTimeout: 10 * time.Second}
	go uiServer.Serve(listener)
	stopTray := startTray(app)
	initErr := app.startPeerChecked(os.Getenv(update.HealthEnv) != "")
	if initErr != nil {
		app.setError(initErr.Error())
	}
	if os.Getenv(update.HealthEnv) != "" {
		if initErr != nil {
			log.Fatal("updated synchronization engine failed initialization: ", initErr)
		}
		// Prove the dashboard handler can actually answer, not merely that bind or
		// process creation succeeded. Never use a proxy for this loopback probe.
		probe := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil}}
		res, probeErr := probe.Get("http://" + uiAddress + "/api/status")
		if probeErr != nil {
			log.Fatal("updated local API health: ", probeErr)
		}
		res.Body.Close()
		probe.CloseIdleConnections()
		if res.StatusCode != http.StatusOK {
			log.Fatal("updated local API is not healthy")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = update.ReportHealthy(ctx)
		cancel()
		if err != nil {
			log.Fatal(err)
		}
	}
	app.startUpdates()
	if !*background {
		openBrowser()
	}
	<-app.quit
	app.stopUpdates()
	stopTray()
	app.stopPeer()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	uiServer.Shutdown(ctx)
}
