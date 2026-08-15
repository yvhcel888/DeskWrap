// DeskWrap CLI (`deskwrap` / `tzj`).
//
//	deskwrap init [dir]     - generate a deskwrap.config.json (auto-detect)
//	deskwrap run [dir]      - run the service inside a desktop window
//	deskwrap build [dir]    - package the service as a desktop app (copy+zip)
//	deskwrap detect [dir]   - print what DeskWrap detected for a project
//	deskwrap gui            - open the desktop GUI (pick folder -> run/build)
//	deskwrap help
package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

// normalizeArg converts MSYS/git-bash style paths (/d/foo -> D:\foo) so the
// CLI works from bash on Windows as well as cmd/PowerShell.
func normalizeArg(p string) string {
	if runtime.GOOS == "windows" {
		if m := regexp.MustCompile(`^/([a-zA-Z])/`).FindStringSubmatch(p); m != nil {
			return strings.ToUpper(m[1]) + ":\\" + strings.ReplaceAll(p[3:], "/", "\\")
		}
	}
	return p
}

func usage() {
	fmt.Print(`DeskWrap - wrap any local web service into a native desktop app
把任意本地 Web 服务一键打包成桌面应用

Usage:
  deskwrap <command> [options]

Commands:
  init [dir]     Generate deskwrap.config.json (auto-detects the project)
  run [dir]      Run the service in a desktop window
  build [dir]    Package the service as a standalone desktop app
  detect [dir]   Show what DeskWrap detected for this project
  gui            Open the desktop GUI (pick a folder, then run/build)
  help           Show this help

Options:
  --config <file>   Use a specific config file
  --out <dir>       Output directory for build artifacts (default: ./release)
  --force           Overwrite existing config on init
`)
}

var cliCommands = map[string]bool{
	"init": true, "run": true, "build": true, "detect": true, "gui": true, "help": true,
}

// cliRun executes a CLI command and returns the process exit code.
func cliRun(args []string) int {
	cmd := args[0]
	rest := args[1:]

	switch cmd {
	case "help", "-h", "--help":
		usage()
		return 0
	case "init":
		return cliInit(rest)
	case "detect":
		return cliDetect(rest)
	case "run":
		return cliRunApp(rest)
	case "build":
		return cliBuild(rest)
	case "gui":
		return cliGui(rest)
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n", cmd)
		usage()
		return 1
	}
}

func firstNonFlag(argv []string) string {
	for _, a := range argv {
		if !strings.HasPrefix(a, "-") {
			return a
		}
	}
	return ""
}

func hasArg(argv []string, name string) bool {
	for _, a := range argv {
		if a == name {
			return true
		}
	}
	return false
}

func argValue(argv []string, name string) string {
	for i, a := range argv {
		if a == name && i+1 < len(argv) {
			return argv[i+1]
		}
	}
	return ""
}

// cliInit interactively generates a config file.
func cliInit(argv []string) int {
	dir := normalizeArg(firstNonFlag(argv))
	if dir == "" {
		dir, _ = os.Getwd()
	}
	force := hasArg(argv, "--force")
	target := filepath.Join(dir, "deskwrap.config.json")
	if fileExists(target) && !force {
		fmt.Fprintf(os.Stderr, "[deskwrap] %s already exists. Use --force to overwrite.\n", target)
		return 1
	}

	d := detect(dir)
	fmt.Printf("[deskwrap] Detected project: %s\n", fmt.Sprint(d["type"]))
	for _, h := range d["hints"].([]string) {
		fmt.Printf("  - %s\n", h)
	}

	reader := bufio.NewReader(os.Stdin)
	ask := func(q, def string) string {
		fmt.Printf("  %s [%s]: ", q, def)
		line, _ := reader.ReadString('\n')
		line = strings.TrimSpace(line)
		if line == "" {
			return def
		}
		return line
	}

	defCmd := fmt.Sprint(d["command"])
	if defCmd == "" || defCmd == "<nil>" {
		defCmd = "npm"
	}
	argsJoined := strings.Join(stringSlice(d["args"]), " ")
	appName := ask("应用名称 / app name", filepath.Base(dir))
	command := ask("启动命令 / start command", defCmd)
	argsRaw := ask("命令参数 / args (空格分隔)", argsJoined)
	portRaw := ask("端口 / port (0=跳过检测)", fmt.Sprintf("%.0f", floatOr(d["port"], 0)))
	trayRaw := ask("托盘模式 / tray mode (y/n)", "n")

	port, _ := strconv.Atoi(strings.TrimSpace(portRaw))
	cfg := map[string]any{
		"appName": appName,
		"service": map[string]any{
			"command":      append([]string{command}, strings.Fields(argsRaw)...),
			"cwd":          filepath.ToSlash(dir),
			"port":         port,
			"readyTimeout": 120000,
			"env":          map[string]any{},
		},
		"window":       map[string]any{"width": 1280, "height": 840, "minWidth": 900, "minHeight": 600},
		"tray":         strings.HasPrefix(strings.ToLower(trayRaw), "y"),
		"autoRestart":  true,
		"showDevTools": false,
	}
	if err := writeRawConfig(target, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "[deskwrap] %v\n", err)
		return 1
	}
	fmt.Printf("\n[deskwrap] Config written to %s\n", target)
	fmt.Printf("[deskwrap] Next: run \"deskwrap run\" (or \"deskwrap build\" to package).\n")
	return 0
}

func stringSlice(v any) []string {
	if arr, ok := v.([]string); ok {
		return arr
	}
	return []string{}
}

func floatOr(v any, def float64) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	}
	return def
}

func cliDetect(argv []string) int {
	dir := normalizeArg(firstNonFlag(argv))
	if dir == "" {
		dir, _ = os.Getwd()
	}
	d := detect(dir)
	fmt.Printf("Project: %s\n", fmt.Sprint(d["dir"]))
	fmt.Printf("Type:    %s\n", fmt.Sprint(d["type"]))
	cmd := fmt.Sprint(d["command"])
	if cmd != "" && cmd != "<nil>" {
		fmt.Printf("Command: %s\n", strings.TrimSpace(cmd+" "+strings.Join(stringSlice(d["args"]), " ")))
	} else {
		fmt.Println("Command: (unknown)")
	}
	port := fmt.Sprintf("%.0f", floatOr(d["port"], 0))
	if port == "0" {
		port = "(unknown - set manually)"
	}
	fmt.Printf("Port:    %s\n", port)
	if hints := d["hints"].([]string); len(hints) > 0 {
		fmt.Println("Hints:")
		for _, h := range hints {
			fmt.Printf("  - %s\n", h)
		}
	}
	if d["type"] == "unknown" {
		fmt.Println("\nTip: no standard project detected. Use \"deskwrap init\" to configure manually.")
	}
	return 0
}

func resolveCliConfig(dir string) (map[string]any, string, int) {
	candidates := []string{}
	if p := os.Getenv("DESKWRAP_CONFIG"); p != "" {
		candidates = append(candidates, p)
	}
	candidates = append(candidates,
		filepath.Join(dir, "deskwrap.config.json"),
		filepath.Join(mustCwd(), "deskwrap.config.json"))
	for _, c := range candidates {
		if fileExists(c) {
			raw, err := readRawConfig(c)
			if err != nil {
				fmt.Fprintf(os.Stderr, "[deskwrap] Invalid config %s: %v\n", c, err)
				return nil, "", 1
			}
			cmd := pickPath(raw, "service.command")
			if !hasServiceCommand(cmd) {
				fmt.Fprintf(os.Stderr, "[deskwrap] Invalid config %s: service.command is empty\n", c)
				return nil, "", 1
			}
			return deepMerge(defaults, raw), c, 0
		}
	}
	fmt.Fprintln(os.Stderr, `[deskwrap] No deskwrap.config.json found. Run "deskwrap init" first.`)
	return nil, "", 1
}

func mustCwd() string {
	d, err := os.Getwd()
	if err != nil {
		return "."
	}
	return d
}

func cliRunApp(argv []string) int {
	dir := normalizeArg(firstNonFlag(argv))
	if dir == "" {
		dir = mustCwd()
	}
	cfg, path, code := resolveCliConfig(dir)
	if code != 0 {
		return code
	}

	cmdArr, _ := resolveCommand(pickPath(cfg, "service.command"))
	_, warnings := checkServiceEnvironment(cmdArr, cfgStr(cfg, "service.cwd"))
	for _, w := range warnings {
		fmt.Fprintf(os.Stderr, "[deskwrap] ⚠ %s\n", w)
	}

	fmt.Printf("[deskwrap] Launching desktop window for %s\n", path)
	return launchSelf("--config", path)
}

func cliGui(argv []string) int {
	fmt.Println("[deskwrap] Opening DeskWrap GUI...")
	return launchSelf("--gui")
}

func cliBuild(argv []string) int {
	dir := normalizeArg(firstNonFlag(argv))
	if dir == "" {
		dir = mustCwd()
	}
	cfg, _, code := resolveCliConfig(dir)
	if code != 0 {
		return code
	}
	if out := argValue(argv, "--out"); out != "" {
		cfg["outDir"] = out
	}
	fmt.Printf("[deskwrap] Packaging %s ...\n", cfgStr(cfg, "appName"))
	res := doBuild(dir, cfg)
	if !res.ok {
		fmt.Fprintf(os.Stderr, "[deskwrap] Build failed: %s\n", res.err)
		return 1
	}
	fmt.Println("[deskwrap] Done. Artifacts:")
	for _, a := range res.artifacts {
		fmt.Println("  " + a)
	}
	return 0
}

// launchSelf re-executes this binary with the given extra args, inheriting
// stdio, and returns the child's exit code.
func launchSelf(extra ...string) int {
	exe := mustExecutable()
	argv := append([]string{exe}, extra...)
	code, err := runWait(exe, argv, nil, "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "[deskwrap] %v\n", err)
		return 1
	}
	return code
}
