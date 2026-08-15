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
		r := resolveWindowsCommand(program, args)
		program, args, viaShell = r.program, r.args, r.viaShell
	}

	cwd := cfgStr(cfg, "service.cwd")
	if cwd == "" {
		cwd, _ = os.Getwd()
	}

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
		time.Sleep(1500 * time.Millisecond)
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

// waitForService polls 127.0.0.1:port until it accepts connections, the
// deadline passes, or the service process dies (exitCh closed).
func waitForService(port int, timeout time.Duration, exitCh <-chan struct{}) bool {
	if port <= 0 {
		return true // no port configured - assume ready after spawn
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 1500*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return true
		}
		select {
		case <-exitCh:
			return false
		case <-time.After(500 * time.Millisecond):
		}
	}
	fmt.Printf("[DeskWrap] Service did not become ready on port %d within %s\n", port, timeout)
	return false
}

func portListening(port int) bool {
	if port <= 0 {
		return false
	}
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 1500*time.Millisecond)
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
