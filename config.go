// DeskWrap - config loading, defaults and persistence.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// defaults is merged under the user config before use.
var defaults = map[string]any{
	"version": float64(1),
	"locale":  "auto",
	"appName": "DeskWrap",
	"service": map[string]any{
		"command":      []any{},
		"cwd":          "",
		"port":         float64(0),
		"readyTimeout": float64(60000),
		"env":          map[string]any{},
	},
	"window": map[string]any{
		"width":     float64(1280),
		"height":    float64(800),
		"minWidth":  float64(800),
		"minHeight": float64(600),
	},
	"tray":         false,
	"autoRestart":  false,
	"showDevTools": false,
	"proxy":        map[string]any{"enabled": false, "url": ""},
	"ai":           map[string]any{"enabled": false, "provider": "glm", "apiKey": "", "model": "", "baseUrl": ""},
}

// deepMerge merges extra into base recursively (plain objects only, like the
// original JS implementation).
func deepMerge(base, extra map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		if m, ok := v.(map[string]any); ok {
			if bm, ok2 := out[k].(map[string]any); ok2 {
				out[k] = deepMerge(bm, m)
				continue
			}
		}
		out[k] = v
	}
	return out
}

// findConfigPath resolves the config file:
// --config arg → DESKWRAP_CONFIG env → exe dir (portable) → cwd.
func findConfigPath() string {
	for i, a := range os.Args {
		if a == "--config" && i+1 < len(os.Args) {
			return os.Args[i+1]
		}
	}
	if p := os.Getenv("DESKWRAP_CONFIG"); p != "" {
		return p
	}
	exeDir := filepath.Dir(mustExecutable())
	cwd, _ := os.Getwd()
	for _, c := range []string{
		filepath.Join(exeDir, "deskwrap.config.json"),
		filepath.Join(cwd, "deskwrap.config.json"),
	} {
		if fileExists(c) {
			return c
		}
	}
	return ""
}

// loadConfig merges the config at path (if any) over the defaults.
// When tolerant is false, a parse error is fatal (app mode).
func loadConfig(path string, tolerant bool) (map[string]any, string) {
	if path == "" {
		path = findConfigPath()
	}
	cfg := deepMerge(map[string]any{}, defaults)
	if path == "" {
		return cfg, ""
	}
	raw, err := readRawConfig(path)
	if err != nil {
		if tolerant {
			fmt.Printf("[DeskWrap] %s: %v\n", t("configLoadFailed"), err)
			return cfg, ""
		}
		fmt.Fprintf(os.Stderr, "[DeskWrap] %s: %v\n", t("configLoadFailed"), err)
		os.Exit(1)
	}
	return deepMerge(defaults, raw), path
}

func readRawConfig(path string) (map[string]any, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, errors.New("failed to read config \"" + path + "\": " + err.Error())
	}
	return m, nil
}

func writeRawConfig(path string, m map[string]any) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// --- typed accessors over dotted paths (e.g. "service.port") ----------------

func pickPath(m map[string]any, path string) any {
	if path == "" {
		return m
	}
	var cur any = m
	for _, k := range strings.Split(path, ".") {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mm[k]
		if cur == nil {
			return nil
		}
	}
	return cur
}

func cfgMap(cfg map[string]any, path string) map[string]any {
	if m, ok := pickPath(cfg, path).(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

func cfgStr(cfg map[string]any, path string) string {
	switch v := pickPath(cfg, path).(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	default:
		return ""
	}
}

func cfgInt(cfg map[string]any, path string) int {
	switch v := pickPath(cfg, path).(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	case int32:
		return int(v)
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(v))
		return n
	default:
		return 0
	}
}

func cfgBool(cfg map[string]any, path string) bool {
	b, _ := pickPath(cfg, path).(bool)
	return b
}

// mustExecutable returns the path of the running executable (fatal on error).
func mustExecutable() string {
	p, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "[DeskWrap] cannot locate executable:", err)
		os.Exit(1)
	}
	return p
}

// resolveServiceCwd resolves service.cwd to an absolute path.  Portable
// builds use a relative cwd ("app") that must be resolved against the
// executable's directory — the packaged app is double-clicked from
// anywhere, so a cwd relative to the *launch* directory would be invalid.
func resolveServiceCwd(cfg map[string]any) string {
	cwd := cfgStr(cfg, "service.cwd")
	if cwd == "" {
		cwd, _ = os.Getwd()
		return cwd
	}
	if !filepath.IsAbs(cwd) {
		cwd = filepath.Join(filepath.Dir(mustExecutable()), cwd)
	}
	return filepath.Clean(cwd)
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}
