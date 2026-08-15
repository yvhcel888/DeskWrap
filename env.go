// DeskWrap - environment checks: the user's own toolchain, VRAM and Node
// version managers. Shared by the GUI and the CLI.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

var runtimeHints = map[string]string{
	"node":   "Node.js: https://nodejs.org/",
	"npm":    "npm ships with Node.js",
	"pnpm":   "install: npm install -g pnpm",
	"yarn":   "install: npm install -g yarn",
	"git":    "Git: https://git-scm.com/",
	"python": "Python: https://www.python.org/",
}

// runVersion executes a command and returns its first stdout line.
func runVersion(cmd string, args []string) string {
	program, cmdArgs, viaShell := cmd, args, false
	if runtime.GOOS == "windows" {
		r := resolveWindowsCommand(cmd, args)
		program, cmdArgs, viaShell = r.program, r.args, r.viaShell
	}
	var c *exec.Cmd
	if viaShell {
		c = shellCommand(program, cmdArgs)
	} else {
		c = exec.Command(program, cmdArgs...)
	}
	applyHiddenFlags(c)
	c.Stdin = nil
	var out strings.Builder
	c.Stdout = &out
	c.Stderr = nil
	done := make(chan struct{})
	go func() {
		_ = c.Run()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		killProcessTree(c.Process.Pid)
		<-done
		return ""
	}
	if c.ProcessState == nil || !c.ProcessState.Success() {
		return ""
	}
	line := strings.TrimSpace(out.String())
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	return line
}

// checkEnvironment reports the versions of the common toolchain.
func checkEnvironment() map[string]any {
	return map[string]any{
		"node":   runVersion("node", []string{"--version"}),
		"npm":    runVersion("npm", []string{"--version"}),
		"pnpm":   runVersion("pnpm", []string{"--version"}),
		"yarn":   runVersion("yarn", []string{"--version"}),
		"git":    runVersion("git", []string{"--version"}),
		"python": runVersion(pythonName(), []string{"--version"}),
	}
}

func pythonName() string {
	if runtime.GOOS == "windows" {
		return "python"
	}
	return "python3"
}

var (
	reNodeMajor   = regexp.MustCompile(`v?(\d+)`)
	reMinNodeMain = regexp.MustCompile(`([>=^~]*)\s*(\d+)`)
)

func nodeMajor(version string) int {
	if m := reNodeMajor.FindStringSubmatch(version); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 0
}

func minNodeMajor(rangeStr string) int {
	if m := reMinNodeMain.FindStringSubmatch(rangeStr); m != nil {
		n, _ := strconv.Atoi(m[2])
		return n
	}
	return 0
}

// runtimeNeeded guesses which runtime a command needs.
func runtimeNeeded(command []string) string {
	if len(command) == 0 {
		return ""
	}
	prog := strings.ToLower(filepath.Base(command[0]))
	switch {
	case prog == "node" || prog == "npm" || prog == "npx" || prog == "pnpm" || prog == "yarn",
		strings.HasSuffix(prog, ".cmd"):
		return "node"
	case strings.HasPrefix(prog, "python"), prog == "uvicorn", prog == "streamlit", prog == "flask":
		return "python"
	case prog == "git":
		return "git"
	}
	return ""
}

// checkServiceEnvironment returns {env, warnings} - warnings to show before
// starting a service whose toolchain is missing or too old.
func checkServiceEnvironment(command []string, cwd string) (map[string]any, []string) {
	env := checkEnvironment()
	warnings := []string{}
	need := runtimeNeeded(command)
	prog := strings.ToLower(filepath.Base(first(command)))

	switch {
	case need == "node" && env["node"] == "":
		warnings = append(warnings, "未检测到 Node.js。请先安装："+runtimeHints["node"])
	case need == "python" && env["python"] == "":
		warnings = append(warnings, "未检测到 Python。请先安装："+runtimeHints["python"])
	}
	switch prog {
	case "pnpm", "pnpm.cmd":
		if env["pnpm"] == "" {
			warnings = append(warnings, "未检测到 pnpm。"+runtimeHints["pnpm"])
		}
	case "yarn", "yarn.cmd":
		if env["yarn"] == "" {
			warnings = append(warnings, "未检测到 yarn。"+runtimeHints["yarn"])
		}
	case "git":
		if env["git"] == "" {
			warnings = append(warnings, "未检测到 Git。"+runtimeHints["git"])
		}
	}

	// engines.node conflict check
	if cwd != "" && env["node"] != "" {
		if b, err := os.ReadFile(filepath.Join(cwd, "package.json")); err == nil {
			var pkg map[string]any
			if json.Unmarshal(b, &pkg) == nil {
				if eng, ok := pkg["engines"].(map[string]any); ok {
					if req, ok := eng["node"].(string); ok && req != "" {
						if nodeMajor(fmt.Sprint(env["node"])) < minNodeMajor(req) {
							warnings = append(warnings, fmt.Sprintf(
								"项目要求 Node.js %s，当前为 %s（版本过低）。建议升级：%s",
								req, env["node"], runtimeHints["node"]))
						}
					}
				}
			}
		}
	}
	return map[string]any{"env": env, "warnings": warnings}, warnings
}

func first(arr []string) string {
	if len(arr) == 0 {
		return ""
	}
	return arr[0]
}

// --- VRAM detection (like ollama's gpu check) -------------------------------

// checkVRAM probes nvidia-smi, then falls back to WMI on Windows.
func checkVRAM() []map[string]any {
	// nvidia-smi first: fast and precise.
	out := runVersion("nvidia-smi", []string{"--query-gpu=name,memory.total", "--format=csv,noheader,nounits"})
	if out != "" {
		gpus := []map[string]any{}
		for _, line := range strings.Split(out, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			m := regexp.MustCompile(`(.*),\s*(\d+)$`).FindStringSubmatch(line)
			if m == nil {
				continue
			}
			mb, _ := strconv.Atoi(m[2])
			if mb > 0 {
				gpus = append(gpus, map[string]any{"name": strings.TrimSpace(m[1]), "vramMB": mb})
			}
		}
		if len(gpus) > 0 {
			return gpus
		}
	}
	// Windows fallback: WMI video controller RAM (approximate).
	if runtime.GOOS == "windows" {
		ps := runPowerShell("Get-CimInstance Win32_VideoController | Select-Object Name,AdapterRAM | ConvertTo-Json -Compress")
		if ps != "" {
			var data any
			if json.Unmarshal([]byte(ps), &data) == nil {
				list := []any{}
				if arr, ok := data.([]any); ok {
					list = arr
				} else {
					list = []any{data}
				}
				gpus := []map[string]any{}
				for _, it := range list {
					m, ok := it.(map[string]any)
					if !ok {
						continue
					}
					ram, _ := m["AdapterRAM"].(float64)
					mb := int(ram / 1048576)
					if mb <= 0 {
						continue
					}
					name, _ := m["Name"].(string)
					if name == "" {
						name = "GPU"
					}
					gpus = append(gpus, map[string]any{"name": name, "vramMB": mb})
				}
				if len(gpus) > 0 {
					return gpus
				}
			}
		}
	}
	return []map[string]any{}
}

func runPowerShell(script string) string {
	c := exec.Command("powershell", "-NoProfile", "-Command", script)
	applyHiddenFlags(c)
	var out strings.Builder
	c.Stdout = &out
	c.Stderr = nil
	done := make(chan struct{})
	go func() { _ = c.Run(); close(done) }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		killProcessTree(c.Process.Pid)
		<-done
		return ""
	}
	return strings.TrimSpace(out.String())
}

// --- Node version manager awareness (nvm / volta / fnm) ---------------------

func detectVersionManager() map[string]any {
	result := map[string]any{}
	for _, name := range []string{"nvm", "volta", "fnm"} {
		if v := runVersion(name, []string{"--version"}); v != "" {
			result[name] = v
		}
	}
	return result
}

func detectNodeVersions() []string {
	versions := []string{}
	home, _ := os.UserHomeDir()
	roots := []string{
		filepath.Join(home, "AppData", "Roaming", "nvm"),
		filepath.Join(home, "AppData", "Local", "nvm"),
		filepath.Join(home, ".nvm"),
		filepath.Join(home, ".volta", "tools", "image", "node"),
		filepath.Join(home, "AppData", "Local", "fnm", "node-versions"),
	}
	seen := map[string]bool{}
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			if regexp.MustCompile(`^v?\d+\.\d+`).MatchString(name) && !seen[name] {
				seen[name] = true
				versions = append(versions, name)
			}
		}
	}
	return versions
}
