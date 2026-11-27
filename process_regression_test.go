package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// isolateServiceState keeps the package-level lifecycle state from leaking
// between regression tests. None of these tests run in parallel.
func isolateServiceState(t *testing.T) {
	t.Helper()
	prevQuit := quitting.Load()

	serviceMu.Lock()
	prevCmd, prevGen, prevExitCh := serviceCmd, serviceGen, serviceExitCh
	serviceCmd, serviceGen, serviceExitCh = nil, 0, nil
	serviceMu.Unlock()
	quitting.Store(false)

	t.Cleanup(func() {
		serviceMu.Lock()
		cmd, exitCh := serviceCmd, serviceExitCh
		serviceMu.Unlock()
		if cmd != nil && cmd.Process != nil {
			killProcessTree(cmd.Process.Pid)
			if exitCh != nil {
				select {
				case <-exitCh:
				case <-time.After(5 * time.Second):
					t.Error("service watcher did not finish during cleanup")
				}
			}
		}
		serviceMu.Lock()
		serviceCmd, serviceGen, serviceExitCh = prevCmd, prevGen, prevExitCh
		serviceMu.Unlock()
		quitting.Store(prevQuit)
	})
}

func TestProcessRegressionHelper(t *testing.T) {
	switch os.Getenv("DW_PROCESS_HELPER") {
	case "argv":
		_ = json.NewEncoder(os.Stdout).Encode(os.Args)
		os.Exit(0)
	case "sleep":
		time.Sleep(30 * time.Second)
		os.Exit(0)
	case "flood":
		chunk := bytes.Repeat([]byte("x"), 8192)
		for i := 0; i < 64; i++ {
			_, _ = os.Stdout.Write(chunk)
		}
		_, _ = os.Stdout.WriteString("\nSTDOUT_END_MARKER\n")
		_, _ = os.Stderr.WriteString("\nSTDERR_END_MARKER\n")
		os.Exit(0)
	}
}

func regressionServiceConfig(mode string) map[string]any {
	exe, err := os.Executable()
	if err != nil {
		panic(err)
	}
	return map[string]any{
		"service": map[string]any{
			"command":     []string{exe, "-test.run=^TestProcessRegressionHelper$"},
			"env":         map[string]any{"DW_PROCESS_HELPER": mode},
			"autoRestart": false,
		},
	}
}

func TestStopServiceLockedInvalidatesGenerationWithoutCommand(t *testing.T) {
	isolateServiceState(t)
	serviceMu.Lock()
	serviceGen = 41
	serviceCmd = nil
	serviceMu.Unlock()

	stopServiceLocked()

	serviceMu.Lock()
	got := serviceGen
	serviceMu.Unlock()
	if got != 42 {
		t.Fatalf("serviceGen = %d, want 42 even when no service command exists", got)
	}
}

func TestShellCommandPreservesArgv(t *testing.T) {
	if runtime.GOOS != "windows" {
		if _, err := os.Stat("/bin/sh"); err != nil {
			t.Skip("/bin/sh is unavailable")
		}
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"two words",
		"a&b",
		"pipe|char",
		"redirect<to>",
		"caret^char",
		`quote"char`,
		`quote"&still-one-arg`,
		`trailing\`,
		`back\slash`,
		"%PATH%",
		"!bang!",
		"$dollar",
		"semi;colon",
		"single'quote",
		"",
	}
	argv := append([]string{"-test.run=^TestProcessRegressionHelper$", "--"}, want...)
	c := shellCommand(exe, argv)
	c.Env = append(os.Environ(), "DW_PROCESS_HELPER=argv")
	out, err := c.Output()
	if err != nil {
		t.Fatalf("shell command failed: %v\noutput: %s", err, out)
	}
	var got []string
	if err := json.Unmarshal(bytes.TrimSpace(out), &got); err != nil {
		t.Fatalf("decode helper argv: %v\noutput: %q", err, out)
	}
	for i := range got {
		if got[i] == "--" {
			got = got[i+1:]
			break
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv changed by shell:\n got: %#q\nwant: %#q", got, want)
	}
}

func TestShellCommandRejectsNewlines(t *testing.T) {
	for _, arg := range []string{"line\nbreak", "carriage\rreturn"} {
		c := shellCommand("tool.cmd", []string{arg})
		if c.Err == nil {
			t.Fatalf("shellCommand accepted newline argument %q", arg)
		}
	}
}

func TestWindowsShellCommandPreservesCmdShimSemantics(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows cmd shim semantics")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "shim with spaces & dollar$")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	shim := filepath.Join(dir, "echo args.cmd")
	body := fmt.Sprintf("@echo off\r\n\"%s\" -test.run=^TestProcessRegressionHelper$ -- %%*\r\n", exe)
	if err := os.WriteFile(shim, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	want := []string{"two words", "a&b", `quote"char`, "%PATH%", ""}
	c := shellCommand(shim, want)
	c.Env = append(os.Environ(), "DW_PROCESS_HELPER=argv")
	out, err := c.Output()
	if err != nil {
		t.Fatalf("cmd shim failed: %v\noutput: %s", err, out)
	}
	var got []string
	if err := json.Unmarshal(bytes.TrimSpace(out), &got); err != nil {
		t.Fatalf("decode helper argv: %v\noutput: %q", err, out)
	}
	for i := range got {
		if got[i] == "--" {
			got = got[i+1:]
			break
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cmd shim argv changed:\n got: %#q\nwant: %#q", got, want)
	}
}

func TestMergedEnvOverridesPathCaseInsensitivelyOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows environment keys are case-insensitive")
	}
	t.Setenv("PATH", "parent-path")
	cfg := map[string]any{"service": map[string]any{"env": map[string]any{"Path": "configured-path"}}}
	env := mergedEnv(cfg)
	var values []string
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if ok && strings.EqualFold(key, "PATH") {
			values = append(values, value)
		}
	}
	if !reflect.DeepEqual(values, []string{"configured-path"}) {
		t.Fatalf("PATH entries = %#q, want one configured override", values)
	}
}

func TestWaitForServiceRejectsClosedExitBeforeDial(t *testing.T) {
	ln, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	exitCh := make(chan struct{})
	close(exitCh)
	if waitForService(port, 2*time.Second, exitCh) {
		t.Fatal("waitForService accepted a listener after the service exited")
	}
}

func TestStartServiceRejectsOccupiedPort(t *testing.T) {
	isolateServiceState(t)
	ln, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	cfg := regressionServiceConfig("sleep")
	svc := cfg["service"].(map[string]any)
	svc["port"] = ln.Addr().(*net.TCPAddr).Port

	if err := startService(cfg); err == nil {
		t.Fatal("startService accepted a port already owned by an unrelated listener")
	}
	serviceMu.Lock()
	cmd := serviceCmd
	serviceMu.Unlock()
	if cmd != nil {
		t.Fatal("startService spawned a command despite the occupied port")
	}
}

func TestServiceExitWaitsForOutputReaders(t *testing.T) {
	isolateServiceState(t)
	serviceLog.reset()
	cfg := regressionServiceConfig("flood")
	if err := startService(cfg); err != nil {
		t.Fatal(err)
	}
	exitCh := currentServiceExitCh()
	select {
	case <-exitCh:
	case <-time.After(15 * time.Second):
		t.Fatal("service did not exit")
	}
	logText := strings.Join(serviceLog.tail(maxLogLines), "\n")
	if !strings.Contains(logText, "STDOUT_END_MARKER") {
		t.Fatal("service exit was published before stdout reached the log")
	}
	if !strings.Contains(logText, "STDERR_END_MARKER") {
		t.Fatal("service exit was published before stderr reached the log")
	}
}

func TestSystemANSIEncodingConcurrent(t *testing.T) {
	start := make(chan struct{})
	results := make(chan any, 32)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results <- systemANSIEncoding()
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	first := <-results
	for got := range results {
		if !reflect.DeepEqual(first, got) {
			t.Fatalf("concurrent ANSI initialization returned inconsistent encodings: first=%#v got=%#v", first, got)
		}
	}
}

func TestWaitForServiceChecksExitAfterSuccessfulDial(t *testing.T) {
	exitCh := make(chan struct{})
	client, peer := net.Pipe()
	defer peer.Close()
	dial := func(network, addr string, timeout time.Duration) (net.Conn, error) {
		close(exitCh)
		return client, nil
	}
	if waitForServiceWithDial(12345, time.Second, exitCh, dial) {
		t.Fatal("waitForService accepted readiness after the child exited during dial")
	}
}

func TestPrependBundledPathKeepsConfiguredPath(t *testing.T) {
	exeDir := t.TempDir()
	nodeDir := filepath.Join(exeDir, "node")
	pythonDir := filepath.Join(exeDir, "python")
	for _, dir := range []string{nodeDir, pythonDir} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	key := "PATH"
	if runtime.GOOS == "windows" {
		key = "Path"
	}
	configured := filepath.Join("configured", "bin") + string(os.PathListSeparator) + "tail"
	env := []string{"KEEP=value", key + "=" + configured}
	got := prependBundledPath(env, exeDir)
	var paths []string
	for _, entry := range got {
		k, value, ok := strings.Cut(entry, "=")
		if ok && ((runtime.GOOS == "windows" && strings.EqualFold(k, "PATH")) || k == "PATH") {
			paths = append(paths, value)
		}
	}
	want := nodeDir + string(os.PathListSeparator) + pythonDir + string(os.PathListSeparator) + configured
	if !reflect.DeepEqual(paths, []string{want}) {
		t.Fatalf("PATH entries = %#q, want one bundled-prefixed configured PATH", paths)
	}
}

func TestRestartServiceRejectsStaleGeneration(t *testing.T) {
	isolateServiceState(t)
	serviceMu.Lock()
	serviceGen = 12
	serviceMu.Unlock()
	cfg := regressionServiceConfig("sleep")
	if err := restartService(cfg, 11); err == nil {
		t.Fatal("stale auto-restart was allowed to spawn a service")
	}
	serviceMu.Lock()
	cmd := serviceCmd
	serviceMu.Unlock()
	if cmd != nil {
		t.Fatal("stale auto-restart spawned a command")
	}
}

func TestPrependBundledPathDoesNotAddCurrentDirectory(t *testing.T) {
	exeDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(exeDir, "node"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := prependBundledPath([]string{"KEEP=value"}, exeDir)
	want := filepath.Join(exeDir, "node")
	if len(got) != 2 || got[0] != "KEEP=value" || got[1] != "PATH="+want {
		t.Fatalf("env = %#v, want PATH without an empty current-directory entry", got)
	}
}

func TestReplaceShimDirTreatsReplacementLiterally(t *testing.T) {
	shimDir := `C:\cash$1\`
	got := replaceShimDir(`%~DP0\node.exe`, shimDir)
	want := shimDir + `\node.exe`
	if got != want {
		t.Fatalf("replaceShimDir = %q, want literal %q", got, want)
	}
}

func TestShutdownServiceMarksQuittingBeforeStop(t *testing.T) {
	isolateServiceState(t)
	serviceMu.Lock()
	serviceGen = 7
	serviceCmd = nil
	serviceMu.Unlock()

	shutdownService()

	if !quitting.Load() {
		t.Fatal("application shutdown did not mark quitting")
	}
	serviceMu.Lock()
	got := serviceGen
	serviceMu.Unlock()
	if got != 8 {
		t.Fatalf("serviceGen = %d, want shutdown to invalidate the watcher", got)
	}
}
