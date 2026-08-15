//go:build !windows

// DeskWrap - GUI fallback for non-Windows platforms: no native window, the
// service just runs in the foreground and the GUI command opens a browser.
// Windows is the primary target; this keeps `go build` green everywhere.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
)

func initPlatform() bool { return true }

func createGuiWindow() {
	cfgMu.RLock()
	port := cfgInt(config, "service.port")
	cfgMu.RUnlock()
	if port > 0 {
		openBrowser(fmt.Sprintf("http://127.0.0.1:%d", port))
	}
	fmt.Println("[DeskWrap] GUI is only available on Windows. The service keeps running; press Ctrl+C to stop.")
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	<-sig
}

func createServiceWindow(cfg map[string]any) {
	port := cfgInt(cfg, "service.port")
	ready := waitForService(port, toDuration(cfgInt(cfg, "service.readyTimeout")), currentServiceExitCh())
	if ready && port > 0 {
		openBrowser(fmt.Sprintf("http://127.0.0.1:%d", port))
	}
	fmt.Println("[DeskWrap] Service running. Press Ctrl+C to stop.")
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	<-sig
}

func ensureServiceWindow(cfg map[string]any) {}

func openBrowser(url string) {
	var c *exec.Cmd
	if runtime.GOOS == "darwin" {
		c = exec.Command("open", url)
	} else {
		c = exec.Command("xdg-open", url)
	}
	_ = c.Start()
}
