package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const launchMarkerEnv = "DESKWRAP_TEST_LAUNCH_MARKER"

// TestMain also acts as the app-mode child spawned by cliRunApp. Recording the
// selected config lets the CLI integration tests verify the re-exec arguments
// without starting a service or opening a WebView.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "--config" {
		if marker := os.Getenv(launchMarkerEnv); marker != "" && len(os.Args) > 2 {
			_ = os.WriteFile(marker, []byte(os.Args[2]), 0o600)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func writeRegressionConfig(t *testing.T, path string, value any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func captureRegressionOutput(t *testing.T, fn func() int) (int, string, string) {
	t.Helper()
	out, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	errOut, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		out.Close()
		t.Fatal(err)
	}
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = out, errOut
	defer func() { os.Stdout, os.Stderr = oldOut, oldErr }()

	code := fn()
	os.Stdout, os.Stderr = oldOut, oldErr
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	if err := errOut.Close(); err != nil {
		t.Fatal(err)
	}
	outBytes, err := os.ReadFile(out.Name())
	if err != nil {
		t.Fatal(err)
	}
	errBytes, err := os.ReadFile(errOut.Name())
	if err != nil {
		t.Fatal(err)
	}
	return code, string(outBytes), string(errBytes)
}

func changeRegressionCwd(t *testing.T, dir string) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(old); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})
}

func TestCliConfigFlagIsPassedToAppMode(t *testing.T) {
	project := t.TempDir()
	selected := filepath.Join(t.TempDir(), "selected.json")
	writeRegressionConfig(t, filepath.Join(project, "deskwrap.config.json"), map[string]any{
		"appName": "Default",
		"service": map[string]any{"command": []string{"default-server"}},
	})
	writeRegressionConfig(t, selected, map[string]any{
		"appName": "Selected",
		"service": map[string]any{"command": []string{"selected-server"}},
	})
	marker := filepath.Join(t.TempDir(), "launch-config")
	t.Setenv(launchMarkerEnv, marker)
	t.Setenv("DESKWRAP_CONFIG", "")
	changeRegressionCwd(t, t.TempDir())

	code, _, stderr := captureRegressionOutput(t, func() int {
		return cliRun([]string{"run", "--config", selected, project})
	})
	if code != 0 {
		t.Fatalf("cliRun = %d, stderr = %s", code, stderr)
	}
	b, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(b), selected; got != want {
		t.Fatalf("app-mode config = %q, want %q", got, want)
	}
}

func TestCliConfigEqualsFlagIsPassedToAppMode(t *testing.T) {
	project := t.TempDir()
	selected := filepath.Join(t.TempDir(), "selected.json")
	writeRegressionConfig(t, filepath.Join(project, "deskwrap.config.json"), map[string]any{
		"appName": "Default",
		"service": map[string]any{"command": []string{"default-server"}},
	})
	writeRegressionConfig(t, selected, map[string]any{
		"appName": "Selected",
		"service": map[string]any{"command": []string{"selected-server"}},
	})
	marker := filepath.Join(t.TempDir(), "launch-config")
	t.Setenv(launchMarkerEnv, marker)
	t.Setenv("DESKWRAP_CONFIG", "")
	changeRegressionCwd(t, t.TempDir())

	code, _, stderr := captureRegressionOutput(t, func() int {
		return cliRun([]string{"run", project, "--config=" + selected})
	})
	if code != 0 {
		t.Fatalf("cliRun = %d, stderr = %s", code, stderr)
	}
	b, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(b), selected; got != want {
		t.Fatalf("app-mode config = %q, want %q", got, want)
	}
}

func TestCliCommandsRejectMissingConfigValue(t *testing.T) {
	for _, cmd := range []string{"run", "build", "detect"} {
		for _, flag := range []string{"--config", "--config=", "--config --force"} {
			t.Run(cmd+"/"+flag, func(t *testing.T) {
				changeRegressionCwd(t, t.TempDir())
				t.Setenv("DESKWRAP_CONFIG", "")
				marker := filepath.Join(t.TempDir(), "launch-config")
				t.Setenv(launchMarkerEnv, marker)

				args := []string{cmd}
				if flag == "--config --force" {
					args = append(args, "--config", "--force")
				} else {
					args = append(args, flag)
				}
				code, _, stderr := captureRegressionOutput(t, func() int { return cliRun(args) })
				if code != 1 {
					t.Fatalf("cliRun = %d, want 1; stderr = %s", code, stderr)
				}
				if !strings.Contains(stderr, "--config requires a file path") {
					t.Fatalf("stderr = %q, want missing-value error", stderr)
				}
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Fatalf("invalid options must not launch app mode: %v", err)
				}
			})
		}
	}
}

func TestExplicitMissingConfigDoesNotFallBack(t *testing.T) {
	project := t.TempDir()
	writeRegressionConfig(t, filepath.Join(project, "deskwrap.config.json"), map[string]any{
		"appName": "Default",
		"service": map[string]any{"command": []string{"default-server"}},
	})
	missing := filepath.Join(t.TempDir(), "missing.json")
	marker := filepath.Join(t.TempDir(), "launch-config")
	t.Setenv(launchMarkerEnv, marker)
	t.Setenv("DESKWRAP_CONFIG", "")
	changeRegressionCwd(t, t.TempDir())

	code, _, stderr := captureRegressionOutput(t, func() int {
		return cliRun([]string{"run", "--config", missing, project})
	})
	if code != 1 {
		t.Fatalf("cliRun = %d, want 1; stderr = %s", code, stderr)
	}
	if !strings.Contains(stderr, missing) {
		t.Fatalf("stderr = %q, want selected config path %q", stderr, missing)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("missing explicit config must not launch app mode: %v", err)
	}
}

func TestCliBuildUsesExplicitConfig(t *testing.T) {
	project := t.TempDir()
	selected := filepath.Join(t.TempDir(), "selected.json")
	out := t.TempDir()
	writeRegressionConfig(t, filepath.Join(project, "deskwrap.config.json"), map[string]any{
		"appName": "Default",
		"service": map[string]any{"command": []string{"default-server"}},
	})
	writeRegressionConfig(t, selected, map[string]any{
		"appName": "Selected",
		"service": map[string]any{"command": []string{"selected-server"}},
	})
	t.Setenv("DESKWRAP_CONFIG", "")
	changeRegressionCwd(t, t.TempDir())

	code, _, stderr := captureRegressionOutput(t, func() int {
		return cliRun([]string{"build", "--config", selected, "--out", out, project})
	})
	if code != 0 {
		t.Fatalf("cliRun = %d, stderr = %s", code, stderr)
	}
	raw, err := readRawConfig(filepath.Join(out, "deskwrap.config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfgStr(raw, "appName"); got != "Selected" {
		t.Fatalf("packaged appName = %q, want Selected", got)
	}
}

func TestCliDetectUsesExplicitConfigDirectory(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "app.py"), []byte("print('hello')\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	selected := filepath.Join(root, "config", "selected.json")
	writeRegressionConfig(t, selected, map[string]any{
		"appName": "Selected",
		"service": map[string]any{"command": []string{"python", "app.py"}, "cwd": "../project"},
	})
	t.Setenv("DESKWRAP_CONFIG", "")
	changeRegressionCwd(t, t.TempDir())

	code, stdout, stderr := captureRegressionOutput(t, func() int {
		return cliRun([]string{"detect", "--config", selected})
	})
	if code != 0 {
		t.Fatalf("cliRun = %d, stderr = %s", code, stderr)
	}
	if !strings.Contains(stdout, "Project: "+project) {
		t.Fatalf("stdout = %q, want configured project %q", stdout, project)
	}
	if !strings.Contains(stdout, "Command: python app.py") {
		t.Fatalf("stdout = %q, want app.py detection", stdout)
	}
}

func TestCliDetectDoesNotTreatConfigValueAsProject(t *testing.T) {
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "package.json"), []byte(`{"scripts":{"dev":"vite"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	selected := filepath.Join(t.TempDir(), "selected.json")
	writeRegressionConfig(t, selected, map[string]any{
		"service": map[string]any{"command": []string{"npm", "run", "dev"}},
	})
	t.Setenv("DESKWRAP_CONFIG", "")
	changeRegressionCwd(t, t.TempDir())

	code, stdout, stderr := captureRegressionOutput(t, func() int {
		return cliRun([]string{"detect", "--config", selected, project})
	})
	if code != 0 {
		t.Fatalf("cliRun = %d, stderr = %s", code, stderr)
	}
	if !strings.Contains(stdout, "Project: "+project) || !strings.Contains(stdout, "Type:    node") {
		t.Fatalf("stdout = %q, want node project %q", stdout, project)
	}
}

func TestAppModeStillFindsExplicitConfig(t *testing.T) {
	selected := filepath.Join(t.TempDir(), "selected.json")
	oldArgs := os.Args
	os.Args = []string{"deskwrap", "--config", selected}
	t.Cleanup(func() { os.Args = oldArgs })
	if got := findConfigPath(); got != selected {
		t.Fatalf("findConfigPath = %q, want %q", got, selected)
	}
}

func TestCliInitStoresAbsoluteProjectDirectory(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	input, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := input.WriteString(strings.Repeat("\n", 5)); err != nil {
		t.Fatal(err)
	}
	if _, err := input.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	oldStdin := os.Stdin
	os.Stdin = input
	t.Cleanup(func() {
		os.Stdin = oldStdin
		input.Close()
	})
	changeRegressionCwd(t, root)

	code, _, stderr := captureRegressionOutput(t, func() int {
		return cliInit([]string{"project", "--force"})
	})
	if code != 0 {
		t.Fatalf("cliInit = %d, stderr = %s", code, stderr)
	}
	raw, err := readRawConfig(filepath.Join(project, "deskwrap.config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := cfgStr(raw, "service.cwd"), filepath.ToSlash(project); got != want {
		t.Fatalf("service.cwd = %q, want absolute %q", got, want)
	}
}

func TestResolveServiceCwdUsesConfigDirectory(t *testing.T) {
	root := t.TempDir()
	configFile := filepath.Join(root, "config", "selected.json")
	writeRegressionConfig(t, configFile, map[string]any{"service": map[string]any{"cwd": "project"}})
	oldPath := configPath
	configPath = configFile
	t.Cleanup(func() { configPath = oldPath })

	cfg := map[string]any{"service": map[string]any{"cwd": "project"}}
	if got, want := resolveServiceCwd(cfg), filepath.Join(root, "config", "project"); got != want {
		t.Fatalf("resolveServiceCwd = %q, want config-relative %q", got, want)
	}
}

func TestResolveServiceCwdWithoutConfigKeepsPortableBase(t *testing.T) {
	oldPath := configPath
	configPath = ""
	t.Cleanup(func() { configPath = oldPath })

	cfg := map[string]any{"service": map[string]any{"cwd": "app"}}
	want := filepath.Join(filepath.Dir(mustExecutable()), "app")
	if got := resolveServiceCwd(cfg); got != want {
		t.Fatalf("resolveServiceCwd = %q, want exe-relative %q", got, want)
	}
}

func TestWriteRawConfigKeepsReadsAndRestrictsPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deskwrap.config.json")
	want := map[string]any{"ai": map[string]any{"apiKey": "secret"}}
	if err := writeRawConfig(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := readRawConfig(path)
	if err != nil {
		t.Fatalf("read newly written config: %v", err)
	}
	if cfgStr(got, "ai.apiKey") != "secret" {
		t.Fatalf("config did not round-trip: %v", got)
	}
	if runtime.GOOS != "windows" {
		assertRegressionConfigMode(t, path)
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := writeRawConfig(path, want); err != nil {
			t.Fatal(err)
		}
		assertRegressionConfigMode(t, path)
	}
}

func assertRegressionConfigMode(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("config mode = %#o, want 0600", got)
	}
}

func TestDetectStreamlitCommand(t *testing.T) {
	for _, entry := range []string{"streamlit_app.py", "app.py"} {
		t.Run(entry, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, entry), []byte("import streamlit as st\nst.write('hi')\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			d := detect(dir)
			if d["type"] != "python" || d["command"] != "streamlit" {
				t.Fatalf("detect = %v, want python/streamlit", d)
			}
			args := stringSlice(d["args"])
			if len(args) != 2 || args[0] != "run" || args[1] != entry {
				t.Fatalf("args = %v, want [run %s]", args, entry)
			}
			if d["port"] != 8501 {
				t.Fatalf("port = %v, want 8501", d["port"])
			}
		})
	}
}

func TestDeepMergeDoesNotAliasInputMaps(t *testing.T) {
	base := map[string]any{
		"untouched": map[string]any{"value": "base"},
		"nested":    map[string]any{"left": "base"},
	}
	extra := map[string]any{
		"added":  map[string]any{"value": "extra"},
		"nested": map[string]any{"right": "extra"},
	}
	out := deepMerge(base, extra)
	out["untouched"].(map[string]any)["value"] = "changed"
	out["added"].(map[string]any)["value"] = "changed"
	out["nested"].(map[string]any)["left"] = "changed"
	out["nested"].(map[string]any)["right"] = "changed"

	if base["untouched"].(map[string]any)["value"] != "base" ||
		extra["added"].(map[string]any)["value"] != "extra" ||
		base["nested"].(map[string]any)["left"] != "base" ||
		extra["nested"].(map[string]any)["right"] != "extra" {
		t.Fatalf("merged config aliases input maps: base=%v extra=%v", base, extra)
	}
}
