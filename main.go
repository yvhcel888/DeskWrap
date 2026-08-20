// DeskWrap - wrap any local web service into a native desktop app.
//
// Two faces of one binary:
//   - CLI: deskwrap init/run/build/detect/gui (see cli.go)
//   - App: with --config or a config next to the exe, spawns the service
//     and opens a native window on it; with --gui opens the management GUI.
//
// The shell is a single small Go executable using the system WebView2
// runtime - no bundled Electron, no bundled Node, no downloads at build
// time. Packaging a project = copying this exe + a config file.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sync"

	"golang.org/x/sys/windows"
)

var (
	config     = deepMerge(map[string]any{}, defaults)
	cfgMu      sync.RWMutex
	configPath string
)

func hasFlag(name string) bool {
	for _, a := range os.Args {
		if a == name {
			return true
		}
	}
	return false
}

var devFlag = hasFlag("--dev")

// enableUTF8Console switches a real Windows console to UTF-8 (CP 65001) so
// the bilingual output renders correctly under chcp 936/437. Piped or
// redirected output is left untouched (pure UTF-8 bytes).
func enableUTF8Console() {
	if runtime.GOOS != "windows" {
		return
	}
	h, err := windows.GetStdHandle(windows.STD_OUTPUT_HANDLE)
	if err != nil {
		return
	}
	var mode uint32
	if windows.GetConsoleMode(h, &mode) != nil {
		return // not a console
	}
	_ = windows.SetConsoleOutputCP(65001)
	_ = windows.SetConsoleCP(65001)
}

func runtimeOS() string {
	switch runtime.GOOS {
	case "windows":
		return "Windows"
	case "darwin":
		return "macOS"
	case "linux":
		return "Linux"
	}
	return runtime.GOOS
}

func main() {
	enableUTF8Console()
	args := os.Args[1:]
	if len(args) > 0 && cliCommands[args[0]] {
		os.Exit(cliRun(args))
		return
	}

	// --- app mode ---
	if hasFlag("--gui") {
		cfg, path := loadConfig("", true)
		cfgMu.Lock()
		config = cfg
		configPath = path
		cfgMu.Unlock()
		runGuiMode()
		return
	}

	cfg, path := loadConfig("", false)
	cfgMu.Lock()
	config = cfg
	configPath = path
	cfgMu.Unlock()

	if !hasServiceCommand(pickPath(cfg, "service.command")) {
		fmt.Println("[DeskWrap] No service command configured; opening GUI picker.")
		runGuiMode()
		return
	}

	if !initPlatform() {
		return // second instance - signal delivered, nothing to do
	}

	// Delay service start when the runtime or dependencies are missing —
	// the service window will show a guidance page and start the service
	// after the runtime/deps are ready (createServiceWindow handles this).
	cmdArr, _ := resolveCommand(pickPath(cfg, "service.command"))
	cwd := resolveServiceCwd(cfg)
	rtMissing := needsRuntimeInstall(cmdArr)
	depsMissing := needsDepsInstall(cwd, cmdArr)
	if rtMissing == "" && depsMissing == "" {
		if err := startService(cfg); err != nil {
			fmt.Fprintln(os.Stderr, "[DeskWrap]", err)
			os.Exit(1)
		}
	}

	createServiceWindow(cfg)
	<-appDone
	stopService()
}

// runGuiMode opens the management GUI (7-panel sidebar) and blocks until it
// closes. Spawned service windows run on their own goroutines.
func runGuiMode() {
	if !initPlatform() {
		return
	}
	createGuiWindow()
	quitting.Store(true)
	stopService()
}

// runWait runs a command with inherited stdio and returns its exit code.
func runWait(program string, argv []string, env []string, dir string) (int, error) {
	c := exec.Command(program, argv[1:]...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	if env != nil {
		c.Env = env
	}
	if dir != "" {
		c.Dir = dir
	}
	err := c.Run()
	if err == nil {
		return 0, nil
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), nil
	}
	return 1, err
}
