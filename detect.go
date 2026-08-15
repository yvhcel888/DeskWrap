// DeskWrap - project detection: figure out how to run a local project and
// which port it likely listens on. Used by `tzj init`, `tzj detect` and the
// GUI. Ported from the original detect.js.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type framework struct {
	match   *regexp.Regexp
	command string
	port    int
}

var webFrameworks = []framework{
	{regexp.MustCompile(`vite`), "npm run dev", 5173},
	{regexp.MustCompile(`next`), "npm run dev", 3000},
	{regexp.MustCompile(`nuxt`), "npm run dev", 3000},
	{regexp.MustCompile(`astro`), "npm run dev", 4321},
	{regexp.MustCompile(`remix`), "npm run dev", 3000},
	{regexp.MustCompile(`svelte`), "npm run dev", 5173},
	{regexp.MustCompile(`vue`), "npm run dev", 5173},
	{regexp.MustCompile(`react`), "npm run dev", 3000},
	{regexp.MustCompile(`webpack`), "npm run dev", 8080},
}

func detect(dir string) map[string]any {
	result := map[string]any{
		"dir":     dir,
		"type":    "unknown",
		"command": "",
		"args":    []string{},
		"port":    0,
		"hints":   []string{},
	}
	hints := func(h string) {
		result["hints"] = append(result["hints"].([]string), h)
	}
	readJSON := func(p string) map[string]any {
		b, err := os.ReadFile(p)
		if err != nil {
			return map[string]any{}
		}
		var m map[string]any
		if json.Unmarshal(b, &m) != nil {
			return map[string]any{}
		}
		return m
	}

	// --- Node.js project ---
	pkgPath := filepath.Join(dir, "package.json")
	if fileExists(pkgPath) {
		pkg := readJSON(pkgPath)
		scripts := map[string]any{}
		if s, ok := pkg["scripts"].(map[string]any); ok {
			scripts = s
		}
		candidates := [][2]string{}
		for _, name := range []string{"dev", "start", "web"} {
			if v, ok := scripts[name].(string); ok && v != "" {
				candidates = append(candidates, [2]string{name, v})
			}
		}

		if len(candidates) > 0 {
			name, script := candidates[0][0], candidates[0][1]
			hasPnpm := fileExists(filepath.Join(dir, "pnpm-lock.yaml"))
			hasYarn := fileExists(filepath.Join(dir, "yarn.lock"))
			runner := "npm"
			if hasPnpm {
				runner = "pnpm"
			} else if hasYarn {
				runner = "yarn"
			}
			result["type"] = "node"
			result["command"] = runner
			if name == "start" {
				result["args"] = []string{"start"}
			} else {
				result["args"] = []string{"run", name}
			}
			hints(`found script "` + name + `" in package.json`)

			combined := name + " " + script
			deps := stringifyMap(pkg["dependencies"]) + stringifyMap(pkg["devDependencies"])
			_, hasDsh := scripts["dsh"]
			if hasDsh || (scripts["web"] != nil && regexp.MustCompile(`dsh|deepseek`).MatchString(deps)) {
				result["command"] = runner
				result["args"] = []string{"dsh", "web"}
				result["port"] = 3080
				hints(`DeepSeek-Harness style project detected, using "dsh web" on port 3080`)
			} else {
				for _, fw := range webFrameworks {
					if fw.match.MatchString(combined) {
						result["port"] = fw.port
						hints("looks like a " + fw.match.String() + " project, defaulting to port " + itoa(fw.port))
						break
					}
				}
			}
		} else {
			// No dev/start script: pnpm workspace with a "dsh" binary?
			isWorkspace := fileExists(filepath.Join(dir, "pnpm-workspace.yaml")) ||
				fileExists(filepath.Join(dir, "pnpm-workspace.yml"))
			found := ""
			foundSub := ""
			if isWorkspace {
				for _, sub := range []string{"apps", "packages", "tools", "cli"} {
					subPath := filepath.Join(dir, sub)
					if !dirExists(subPath) {
						continue
					}
					for _, name := range safeReaddir(subPath) {
						subPkg := filepath.Join(subPath, name, "package.json")
						if !fileExists(subPkg) {
							continue
						}
						sp := readJSON(subPkg)
						if bin, ok := sp["bin"].(map[string]any); ok {
							for k := range bin {
								if regexp.MustCompile(`dsh|desk|web`).MatchString(k) {
									found, foundSub = k, sub
									break
								}
							}
						}
						if found != "" {
							break
						}
					}
					if found != "" {
						break
					}
				}
			}
			if found != "" {
				runner := "npm"
				if fileExists(filepath.Join(dir, "pnpm-lock.yaml")) {
					runner = "pnpm"
				}
				result["type"] = "node"
				result["command"] = runner
				result["args"] = []string{found, "web"}
				result["port"] = 3080
				hints("pnpm workspace detected; " + found + " binary found in " + foundSub + "/ (assuming port 3080)")
			} else if isWorkspace {
				hints("pnpm workspace found but no recognizable web CLI binary")
			} else {
				hints("package.json found but no dev/start script")
			}
		}
	}

	// --- Python project (also runs when no Node project matched) ---
	if result["type"] == "unknown" {
		files := safeReaddir(dir)
		hasPy := func(name string) bool {
			for _, f := range files {
				if f == name {
					return true
				}
			}
			return false
		}
		pyStreamlit := false
		for _, f := range files {
			if strings.HasPrefix(f, "streamlit") && strings.HasSuffix(f, ".py") {
				pyStreamlit = true
				break
			}
		}
		switch {
		case hasPy("streamlit_app.py") || pyStreamlit:
			result["type"] = "python"
			result["command"] = "streamlit"
			result["args"] = []string{"run", "app.py"}
			result["port"] = 8501
			hints("streamlit app detected")
		case hasPy("manage.py"):
			result["type"] = "python"
			result["command"] = "python"
			result["args"] = []string{"manage.py", "runserver"}
			result["port"] = 8000
			hints("django project detected")
		case hasPy("app.py"):
			src, _ := os.ReadFile(filepath.Join(dir, "app.py"))
			s := string(src)
			port := 5000
			switch {
			case strings.Contains(s, "gradio"):
				port = 7860
			case strings.Contains(s, "fastapi") || strings.Contains(s, "uvicorn"):
				port = 8000
			case strings.Contains(s, "streamlit"):
				port = 8501
			}
			result["type"] = "python"
			result["command"] = "python"
			result["args"] = []string{"app.py"}
			result["port"] = port
			hints("app.py found, guessed port " + itoa(port))
		}
	}

	// --- Go ---
	if result["type"] == "unknown" {
		for _, f := range safeReaddir(dir) {
			if f == "main.go" {
				result["type"] = "go"
				result["command"] = "go"
				result["args"] = []string{"run", "."}
				hints("go project detected (port unknown, set it manually)")
				break
			}
		}
	}

	return result
}

func safeReaddir(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []string{}
	}
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name()
	}
	return names
}

func stringifyMap(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func itoa(n int) string {
	return fmt.Sprintf("%d", n)
}
