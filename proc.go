// DeskWrap - service lifecycle (cross-platform part).
//
// Windows specifics (cmd-shim unwrapping, CREATE_NO_WINDOW, ACP decoding,
// taskkill) live in proc_windows.go; the Unix fallbacks in proc_unix.go.
package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var (
	quitting atomic.Bool

	serviceMu     sync.Mutex
	serviceCmd    *exec.Cmd
	serviceGen    int
	serviceExitCh chan struct{} // closed when the current service dies
)

// resolveCommand accepts a string ("npm run dev") or a string array.
func resolveCommand(cmd any) ([]string, error) {
	switch v := cmd.(type) {
	case []any:
		if len(v) == 0 {
			return nil, fmt.Errorf("config.service.command must be a non-empty string or string array")
		}
		out := make([]string, len(v))
		for i, c := range v {
			out[i] = fmt.Sprint(c)
		}
		return out, nil
	case []string:
		if len(v) == 0 {
			return nil, fmt.Errorf("config.service.command must be a non-empty string or string array")
		}
		return append([]string{}, v...), nil
	case string:
		if strings.TrimSpace(v) == "" {
			return nil, fmt.Errorf("config.service.command must be a non-empty string or string array")
		}
		return strings.Fields(v), nil
	}
	return nil, fmt.Errorf("config.service.command must be a non-empty string or string array")
}

func hasServiceCommand(cmd any) bool {
	if arr, ok := cmd.([]any); ok {
		return len(arr) > 0
	}
	if s, ok := cmd.(string); ok {
		return strings.TrimSpace(s) != ""
	}
	return false
}

// mergedEnv = current process env + config.service.env (config wins).
func mergedEnv(cfg map[string]any) []string {
	env := os.Environ()
	extra := cfgMap(cfg, "service.env")
	for k, v := range extra {
		env = append(env, k+"="+fmt.Sprint(v))
	}
	return env
}

// startService spawns the configured command. It returns nil on success.
// On Windows the command is unwrapped (node <entry.js>) so no console
// window ever flashes and paths with spaces are safe.
func startService(cfg map[string]any) error {
	serviceMu.Lock()
	defer serviceMu.Unlock()

	if serviceCmd != nil && serviceCmd.Process != nil {
		stopServiceLocked()
	}

	cmd, err := resolveCommand(pickPath(cfg, "service.command"))
	if err != nil {
		return err
	}

	program, args := cmd[0], cmd[1:]
	viaShell := runtime.GOOS != "windows"
	if runtime.GOOS == "windows" {
		// Portable builds ship a bundled Node.js in <exe_dir>/node/ and a
		// bundled Python in <exe_dir>/python/. Go's exec.Command resolves
		// "node"/"python" against the *parent* PATH at Start() time (not
		// c.Env), so on a machine without them we must point the program at
		// the bundled binary explicitly.
		progBase := strings.ToLower(filepath.Base(program))
		if (progBase == "node" || progBase == "node.exe" || progBase == "python" || progBase == "python3" || progBase == "python.exe") && !strings.ContainsAny(program, `\/`) {
			if progBase == "node" || progBase == "node.exe" {
				if bundled := filepath.Join(filepath.Dir(mustExecutable()), "node", "node.exe"); fileExists(bundled) {
					program = bundled
				}
			} else {
				if bundled := filepath.Join(filepath.Dir(mustExecutable()), "python", "python.exe"); fileExists(bundled) {
					program = bundled
				}
			}
		}
		r := resolveWindowsCommand(program, args)
		program, args, viaShell = r.program, r.args, r.viaShell
	}

	cwd := resolveServiceCwd(cfg)

	fmt.Printf("[DeskWrap] %s: %s %s\n", t("startService"), program, strings.Join(args, " "))
	fmt.Printf("[DeskWrap] %s: %s\n", t("workDir"), cwd)

	var c *exec.Cmd
	if viaShell {
		c = shellCommand(program, args)
	} else {
		c = exec.Command(program, args...)
	}
	c.Dir = cwd
	c.Env = mergedEnv(cfg)
	// Prepend the bundled node/ and python/ directories to PATH so the
	// packaged binaries are found first (the recipient may not have them).
	exeDir := filepath.Dir(mustExecutable())
	extra := ""
	for _, sub := range []string{"node", "python"} {
		if dirExists(filepath.Join(exeDir, sub)) {
			extra += filepath.Join(exeDir, sub) + string(os.PathListSeparator)
		}
	}
	if extra != "" {
		c.Env = append(c.Env, "PATH="+extra+os.Getenv("PATH"))
	}
	applyHiddenFlags(c)

	stdout, err := c.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := c.StderrPipe()
	if err != nil {
		return err
	}

	if err := c.Start(); err != nil {
		return fmt.Errorf("failed to spawn service: %w", err)
	}

	serviceCmd = c
	serviceGen++
	serviceExitCh = make(chan struct{})

	go streamToLog(stdout, &serviceLog, "[service] ")
	go streamToLog(stderr, &serviceLog, "[service] ")
	go watchServiceExit(c, serviceGen, cfg)

	return nil
}

// watchServiceExit waits for the service, then optionally auto-restarts it.
// gen guards against restarts racing with a deliberate stop (the bug in the
// old implementation where "retry" + autoRestart spawned two services).
func watchServiceExit(c *exec.Cmd, gen int, cfg map[string]any) {
	_ = c.Wait()

	serviceMu.Lock()
	sameGen := serviceGen == gen
	if sameGen {
		serviceCmd = nil
		close(serviceExitCh)
	}
	serviceMu.Unlock()
	if !sameGen {
		return
	}

	code := "?"
	if c.ProcessState != nil {
		code = c.ProcessState.String()
	}
	fmt.Printf("[DeskWrap] %s %s\n", t("serviceExited"), code)

	if !quitting.Load() && cfgBool(cfg, "autoRestart") {
		fmt.Printf("[DeskWrap] %s\n", t("autoRestarting"))
		time.Sleep(500 * time.Millisecond)
		if !quitting.Load() {
			_ = startService(cfg)
		}
	}
}

func stopService() {
	serviceMu.Lock()
	defer serviceMu.Unlock()
	stopServiceLocked()
}

func stopServiceLocked() {
	if serviceCmd == nil || serviceCmd.Process == nil {
		serviceCmd = nil
		return
	}
	fmt.Printf("[DeskWrap] %s\n", t("stoppingService"))
	killProcessTree(serviceCmd.Process.Pid)
	serviceCmd = nil
	serviceGen++ // invalidate the exit watcher of the old process
}

// needsDepsInstall checks whether the project's dependencies appear to be
// missing.  Returns the suggested install command ("pnpm install" etc.) or
// "" if deps look present.
//
// The check uses robust markers that survive the "pnpm run creates a bare
// node_modules" case: pnpm stores actual deps in node_modules/.pnpm/,
// npm writes node_modules/.package-lock.json after install, and yarn
// writes node_modules/.yarn-integrity.
func needsDepsInstall(dir string, command []string) string {
	if len(command) == 0 {
		return ""
	}
	// If the runtime itself is missing, deps check is moot — let the caller
	// handle the runtime-missing case first.
	if rt := needsRuntimeInstall(command); rt != "" {
		return ""
	}
	prog := strings.ToLower(filepath.Base(command[0]))
	switch {
	case prog == "pnpm" || prog == "pnpm.cmd":
		if !dirExists(filepath.Join(dir, "node_modules", ".pnpm")) {
			return "pnpm install"
		}
	case prog == "npm" || prog == "npm.cmd" || prog == "npx" || prog == "npx.cmd":
		if !fileExists(filepath.Join(dir, "node_modules", ".package-lock.json")) &&
			!fileExists(filepath.Join(dir, "node_modules", ".yarn-integrity")) {
			return "npm install"
		}
	case prog == "yarn" || prog == "yarn.cmd":
		if !fileExists(filepath.Join(dir, "node_modules", ".yarn-integrity")) {
			return "yarn install"
		}
	case prog == "node" || prog == "node.exe":
		if fileExists(filepath.Join(dir, "package-lock.json")) &&
			!fileExists(filepath.Join(dir, "node_modules", ".package-lock.json")) {
			return "npm install"
		}
		if fileExists(filepath.Join(dir, "pnpm-lock.yaml")) &&
			!dirExists(filepath.Join(dir, "node_modules", ".pnpm")) {
			return "pnpm install"
		}
	case prog == "pip" || prog == "pip3" || prog == "python" || prog == "python3":
		// Portable builds ship a bundled Python with dependencies already
		// installed — never show a "pip install" hint to the recipient.
		if bundled := filepath.Join(filepath.Dir(mustExecutable()), "python", "python.exe"); fileExists(bundled) {
			return ""
		}
		req := filepath.Join(dir, "requirements.txt")
		if fileExists(req) {
			return "pip install -r requirements.txt"
		}
	}
	return ""
}

// needsRuntimeInstall checks whether the required runtime (node, python, etc.)
// is available on the system.  Returns a human-readable guidance string like
// "需要安装 Node.js: https://nodejs.org/" if the runtime is missing, or "" if
// it is found.
func needsRuntimeInstall(command []string) string {
	if len(command) == 0 {
		return ""
	}
	prog := strings.ToLower(filepath.Base(command[0]))
	var name string
	switch {
	case prog == "node" || prog == "node.exe":
		name = "node"
	case prog == "pnpm" || prog == "pnpm.cmd":
		name = "pnpm"
	case prog == "npm" || prog == "npm.cmd" || prog == "npx" || prog == "npx.cmd":
		name = "npm"
	case prog == "yarn" || prog == "yarn.cmd":
		name = "yarn"
	case prog == "python" || prog == "python3" || prog == "pip" || prog == "pip3":
		name = "python"
	case prog == "git":
		name = "git"
	default:
		return ""
	}
	// Portable builds ship a bundled Node.js in <exe_dir>/node/ and a
	// bundled Python in <exe_dir>/python/. When present, the runtime counts
	// as provided even if the recipient's PATH has nothing installed —
	// startService points the program at the bundled binary explicitly.
	if name == "node" || name == "pnpm" || name == "npm" || name == "yarn" {
		if bundled := filepath.Join(filepath.Dir(mustExecutable()), "node", "node.exe"); fileExists(bundled) {
			return ""
		}
	}
	if name == "python" {
		if bundled := filepath.Join(filepath.Dir(mustExecutable()), "python", "python.exe"); fileExists(bundled) {
			return ""
		}
	}
	// Check if the binary exists on PATH.
	binName := name
	if name == "python" && runtime.GOOS == "windows" {
		binName = "python"
	}
	if _, err := exec.LookPath(binName); err == nil {
		return ""
	}
	if name == "python" && runtime.GOOS != "windows" {
		if _, err := exec.LookPath("python3"); err == nil {
			return ""
		}
	}
	if hint, ok := runtimeHints[name]; ok {
		return "需要安装 " + hint
	}
	return "需要安装 " + name
}

// waitForService polls localhost:port until it accepts connections, the
// deadline passes, or the service process dies (exitCh closed).
// Uses "localhost" instead of "127.0.0.1" so Vite 8+ (which defaults to
// IPv6 localhost ::1) is detected correctly.
func waitForService(port int, timeout time.Duration, exitCh <-chan struct{}) bool {
	if port <= 0 {
		return true // no port configured - assume ready after spawn
	}
	deadline := time.Now().Add(timeout)
	addr := fmt.Sprintf("localhost:%d", port)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return true
		}
		select {
		case <-exitCh:
			return false
		case <-time.After(300 * time.Millisecond):
		}
	}
	fmt.Printf("[DeskWrap] Service did not become ready on port %d within %s\n", port, timeout)
	return false
}

func portListening(port int) bool {
	if port <= 0 {
		return false
	}
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", port), 500*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// toDuration converts a millisecond count to a duration with a 60s floor.
func toDuration(ms int) time.Duration {
	if ms <= 0 {
		return 60 * time.Second
	}
	return time.Duration(ms) * time.Millisecond
}

func currentServiceExitCh() <-chan struct{} {
	serviceMu.Lock()
	defer serviceMu.Unlock()
	return serviceExitCh
}
