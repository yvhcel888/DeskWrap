//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestGUIAssetsHaveRestrictiveCSP(t *testing.T) {
	cspRE := regexp.MustCompile(`(?is)<meta\s+http-equiv=["']Content-Security-Policy["']\s+content="([^"]*)"`)
	for name, html := range map[string]string{"gui": guiHTML, "error": errorHTML} {
		t.Run(name, func(t *testing.T) {
			match := cspRE.FindStringSubmatch(html)
			if len(match) != 2 {
				t.Fatal("missing Content-Security-Policy meta tag")
			}
			csp := match[1]
			for _, directive := range []string{
				"default-src 'none'",
				"script-src 'unsafe-inline'",
				"style-src 'unsafe-inline'",
				"object-src 'none'",
				"base-uri 'none'",
				"form-action 'none'",
			} {
				if !strings.Contains(csp, directive) {
					t.Errorf("CSP missing %q: %s", directive, csp)
				}
			}
		})
	}
}

func TestGUIAssetsAvoidHTMLParsingSinks(t *testing.T) {
	sinkRE := regexp.MustCompile(`(?i)\.(innerHTML|outerHTML)\s*=|\binsertAdjacentHTML\s*\(|\bdocument\.write\s*\(`)
	for name, html := range map[string]string{"gui": guiHTML, "error": errorHTML} {
		if match := sinkRE.FindString(html); match != "" {
			t.Errorf("%s.html contains dangerous HTML parsing sink %q", name, match)
		}
	}
}

func TestGUIPlatformRemoteDataRendersAsText(t *testing.T) {
	for _, want := range []string{
		"nameEl.textContent = it.name;",
		"descEl.textContent = stars + (it.desc || '');",
		"st.textContent = '部署失败: ' + dep.error + ' — ';",
		"st.appendChild(deployAiBtn);",
	} {
		if !strings.Contains(guiHTML, want) {
			t.Errorf("gui.html missing safe remote-data rendering %q", want)
		}
	}
}

func jsFunctionBody(t *testing.T, source, name string) string {
	t.Helper()
	marker := "function " + name + "("
	start := strings.Index(source, marker)
	if start < 0 {
		t.Fatalf("missing JavaScript function %s", name)
	}
	open := strings.IndexByte(source[start:], '{')
	if open < 0 {
		t.Fatalf("missing body for JavaScript function %s", name)
	}
	open += start
	depth := 0
	for i := open; i < len(source); i++ {
		switch source[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return source[open+1 : i]
			}
		}
	}
	t.Fatalf("unterminated JavaScript function %s", name)
	return ""
}

func TestGUICollectConfigPreservesUneditedRawFields(t *testing.T) {
	body := jsFunctionBody(t, guiHTML, "collectConfig")
	for _, forbidden := range []string{
		"readyTimeout: 120000",
		"env: {}",
		"showDevTools: false",
		"window: { width: 1280",
	} {
		if strings.Contains(body, forbidden) {
			t.Errorf("collectConfig still discards raw configuration with %q", forbidden)
		}
	}
	for _, want := range []string{
		"cloneConfig(currentRawConfig)",
		"cfg.service = service;",
		"cfg.window = windowCfg;",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("collectConfig missing raw-config preservation %q", want)
		}
	}
	if !strings.Contains(guiHTML, "service.cwd") || !strings.Contains(guiHTML, "currentRawConfig = cfg;") {
		t.Error("GUI must cache raw config and populate service.cwd")
	}
}

func TestGUIProviderChangeClearsPreviousCredentials(t *testing.T) {
	start := strings.Index(guiHTML, "sel.onchange = function (userChanged) {")
	end := strings.Index(guiHTML[start:], "refreshModelList();")
	if start < 0 || end < 0 {
		t.Fatal("missing AI provider change handler")
	}
	handler := guiHTML[start : start+end]
	for _, want := range []string{"aiKey').value = ''", "aiModel').value = ''", "aiBase').value = ''"} {
		if !strings.Contains(handler, want) {
			t.Errorf("provider change does not clear old credential field: %s", want)
		}
	}
}

func TestGUICheckSaveAndLocaleResults(t *testing.T) {
	for _, want := range []string{
		"var res = await window.deskwrap.setLocale(v);",
		"if (!res.ok)",
		"var res = await window.deskwrap.saveConfig(currentDir, cfg);",
		"保存失败: ' + (res.error || '')",
	} {
		if !strings.Contains(guiHTML, want) {
			t.Errorf("gui.html missing result handling %q", want)
		}
	}
}

func setGUIConfigForTest(t *testing.T, cfg map[string]any, path string) {
	t.Helper()
	cfgMu.Lock()
	oldConfig, oldPath := config, configPath
	config, configPath = cfg, path
	cfgMu.Unlock()
	t.Cleanup(func() {
		cfgMu.Lock()
		config, configPath = oldConfig, oldPath
		cfgMu.Unlock()
	})
}

func TestGuiSetLocaleRejectsUnsupportedLocale(t *testing.T) {
	setGUIConfigForTest(t, map[string]any{"locale": "auto"}, filepath.Join(t.TempDir(), "deskwrap.config.json"))

	res := guiSetLocale("fr-FR")
	if res["ok"] != false || res["error"] == "" {
		t.Fatalf("guiSetLocale accepted an unsupported locale: %v", res)
	}
}

func TestGuiSetLocaleReportsReadFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deskwrap.config.json")
	if err := os.WriteFile(path, []byte(`{"broken":`), 0o644); err != nil {
		t.Fatal(err)
	}
	setGUIConfigForTest(t, map[string]any{"locale": "auto"}, path)

	res := guiSetLocale("en-US")
	if res["ok"] != false || res["error"] == "" {
		t.Fatalf("guiSetLocale hid a config read failure: %v", res)
	}
}

func TestGuiSetLocaleReportsWriteFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deskwrap.config.json")
	if err := os.WriteFile(path, []byte(`{"locale":"auto"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })
	setGUIConfigForTest(t, map[string]any{"locale": "auto"}, path)

	res := guiSetLocale("en-US")
	if res["ok"] != false || res["error"] == "" {
		t.Fatalf("guiSetLocale hid a config write failure: %v", res)
	}
}

func TestGuiSetLocaleWithoutPathPersists(t *testing.T) {
	const childEnv = "DESKWRAP_GUI_LOCALE_TEST_CHILD"
	if os.Getenv(childEnv) == "1" {
		setGUIConfigForTest(t, map[string]any{"locale": "auto"}, "")
		res := guiSetLocale("en-US")
		if res["ok"] != true {
			t.Fatalf("guiSetLocale failed with the portable config path: %v", res)
		}
		return
	}

	// Run from a copied test binary so the portable config is created in an
	// isolated directory, never beside the repository's real executable.
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	copyPath := filepath.Join(dir, "deskwrap-locale-test.exe")
	if err := os.WriteFile(copyPath, data, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(copyPath, "-test.run=^TestGuiSetLocaleWithoutPathPersists$")
	cmd.Env = append(os.Environ(), childEnv+"=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("locale child test failed: %v\n%s", err, out)
	}
	path := filepath.Join(dir, "deskwrap.config.json")
	raw, err := readRawConfig(path)
	if err != nil {
		t.Fatalf("locale was not persisted when no config path was loaded: %v", err)
	}
	if raw["locale"] != "en-US" {
		t.Fatalf("persisted locale = %v, want en-US", raw["locale"])
	}
}

func TestTrayQuitUsesApplicationShutdown(t *testing.T) {
	source, err := os.ReadFile("gui_windows.go")
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(string(source), "case <-mQuit.ClickedCh:")
	end := strings.Index(string(source)[start:], "return")
	if start < 0 || end < 0 {
		t.Fatal("missing tray quit handler")
	}
	handler := string(source)[start : start+end]
	for _, want := range []string{"shutdownService()", "w := guiWin", "guiWin = nil", "sw.Destroy()", "w.Destroy()"} {
		if !strings.Contains(handler, want) {
			t.Errorf("tray quit handler missing %q", want)
		}
	}
}

func TestGuiRunAppReplacesOldServiceWindow(t *testing.T) {
	source, err := os.ReadFile("gui_windows.go")
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(string(source), "func guiRunApp(")
	end := strings.Index(string(source)[start:], "\nfunc guiHWND")
	if start < 0 || end < 0 {
		t.Fatal("missing guiRunApp")
	}
	body := string(source)[start : start+end]
	if !strings.Contains(body, "replaceServiceWindow(cfg)") || strings.Contains(body, "ensureServiceWindow(cfg)") {
		t.Error("running a new configuration must replace the old service window")
	}
}

func TestReplaceServiceWindowForcesOldWindowClosed(t *testing.T) {
	source, err := os.ReadFile("gui_windows.go")
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(string(source), "func replaceServiceWindow(")
	end := strings.Index(string(source)[start:], "\nfunc ensureServiceWindow")
	if start < 0 || end < 0 {
		t.Fatal("missing replaceServiceWindow")
	}
	body := string(source)[start : start+end]
	closeAt := strings.Index(body, "closeToTray.Store(false)")
	destroyAt := strings.Index(body, "old.Destroy()")
	if closeAt < 0 || destroyAt < 0 || closeAt > destroyAt {
		t.Error("old service window must leave tray mode before it is destroyed")
	}
}

func TestTrayStartupIsSingleInstance(t *testing.T) {
	source, err := os.ReadFile("gui_windows.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(source), "trayRunning.CompareAndSwap(false, true)") {
		t.Error("tray startup must be guarded against duplicate systray.Run calls")
	}
}

func TestServiceWindowShowLoopStopsWhenWindowCloses(t *testing.T) {
	source, err := os.ReadFile("gui_windows.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"windowDone := make(chan struct{})", "case <-windowDone:", "close(windowDone)"} {
		if !strings.Contains(string(source), want) {
			t.Errorf("service window show loop missing %q", want)
		}
	}
}

func TestServiceWindowBindingsRequireToken(t *testing.T) {
	source, err := os.ReadFile("gui_windows.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`w.Bind("__dw_retry", func(token string)`,
		`w.Bind("__dw_installDeps", func(token string)`,
		"validServiceToken(token, serviceToken)",
	} {
		if !strings.Contains(string(source), want) {
			t.Errorf("service window binding missing token guard %q", want)
		}
	}
}

func TestServiceTokenIsRandomAndValidates(t *testing.T) {
	first := newServiceToken()
	second := newServiceToken()
	if len(first) < 32 || first == second {
		t.Fatalf("service tokens are not random: %q %q", first, second)
	}
	if !validServiceToken(first, first) {
		t.Fatal("service token did not validate against itself")
	}
	if validServiceToken(first, second) || validServiceToken("", first) || validServiceToken(first, "") {
		t.Fatal("service token validation accepted missing or mismatched values")
	}
}

func TestErrorPagePassesTokenToBindings(t *testing.T) {
	for _, want := range []string{
		"__DW_SERVICE_TOKEN__",
		"__dw_retry(window.__DW_TOKEN__)",
		"__dw_installDeps(window.__DW_TOKEN__)",
	} {
		if !strings.Contains(errorHTML, want) {
			t.Errorf("error.html missing token-aware binding call %q", want)
		}
	}
}

func TestWebviewWindowDebugHonorsConfig(t *testing.T) {
	source, err := os.ReadFile("gui_windows.go")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`Debug:\s*devFlag\s*\|\|\s*cfgBool\(cfg,\s*"showDevTools"\)`).Match(source) {
		t.Error("WebView2 Debug must honor devFlag or showDevTools")
	}
}

func TestValidateWebviewDownload(t *testing.T) {
	pe := make([]byte, 64)
	pe[0], pe[1] = 'M', 'Z'
	for _, tc := range []struct {
		name   string
		status int
		data   []byte
		wantOK bool
	}{
		{name: "HTTP success", status: 200, data: pe, wantOK: true},
		{name: "HTTP redirect is not followed here", status: 302, data: pe},
		{name: "HTTP error page", status: 404, data: []byte("not found")},
		{name: "empty response", status: 200},
		{name: "signature only is truncated", status: 200, data: []byte("MZ")},
		{name: "short DOS header", status: 200, data: append([]byte("MZ"), make([]byte, 61)...)},
		{name: "wrong signature", status: 200, data: make([]byte, 64)},
		{name: "HTML error with success status", status: 200, data: []byte(strings.Repeat("<", 64))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateWebviewDownload(tc.status, tc.data)
			if (err == nil) != tc.wantOK {
				t.Fatalf("validateWebviewDownload(%d, %d bytes) = %v, want ok=%v", tc.status, len(tc.data), err, tc.wantOK)
			}
		})
	}
}

func TestGuiSaveConfigReportsWriteFailure(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "deskwrap.config.json")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	setGUIConfigForTest(t, map[string]any{"locale": "auto"}, "")

	res := guiSaveConfig(dir, map[string]any{"appName": "test"})
	if res["ok"] != false || res["error"] == "" {
		t.Fatalf("guiSaveConfig hid a write failure: %v", res)
	}
}

func TestGuiSaveConfigPersistsRawFields(t *testing.T) {
	dir := t.TempDir()
	cfg := map[string]any{
		"appName":      "test",
		"showDevTools": true,
		"service": map[string]any{
			"env":          map[string]any{"KEEP": "value"},
			"readyTimeout": float64(45000),
		},
		"window": map[string]any{"width": float64(1000), "height": float64(700)},
	}
	setGUIConfigForTest(t, map[string]any{"locale": "auto"}, "")

	res := guiSaveConfig(dir, cfg)
	if res["ok"] != true {
		t.Fatalf("guiSaveConfig failed: %v", res)
	}
	raw, err := readRawConfig(filepath.Join(dir, "deskwrap.config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if raw["showDevTools"] != true || cfgStr(raw, "service.env.KEEP") != "value" ||
		cfgInt(raw, "service.readyTimeout") != 45000 || cfgInt(raw, "window.width") != 1000 {
		t.Fatalf("raw fields were not preserved: %v", raw)
	}
}

func TestGuiReadConfigReturnsRawProjectConfig(t *testing.T) {
	dir := t.TempDir()
	res := guiReadConfig(dir)
	if res["ok"] != true || res["config"] != nil {
		t.Fatalf("missing config should be an empty successful read: %v", res)
	}
	cfg := map[string]any{"service": map[string]any{"cwd": "app", "env": map[string]any{"A": "B"}}}
	if err := writeRawConfig(filepath.Join(dir, "deskwrap.config.json"), cfg); err != nil {
		t.Fatal(err)
	}
	res = guiReadConfig(dir)
	raw, _ := res["config"].(map[string]any)
	if res["ok"] != true || cfgStr(raw, "service.cwd") != "app" || cfgStr(raw, "service.env.A") != "B" {
		t.Fatalf("guiReadConfig did not return raw project fields: %v", res)
	}
}

func TestWebviewInstallGuardsDownloadAndProcess(t *testing.T) {
	source, err := os.ReadFile("gui_windows.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"data, status, err := appFetch(",
		"validateWebviewDownload(status, data)",
		"os.CreateTemp(",
		"if c.Process != nil {",
	} {
		if !strings.Contains(string(source), want) {
			t.Errorf("WebView2 installer missing guard %q", want)
		}
	}
}

func TestSaveConfigBindingReturnsStructuredErrors(t *testing.T) {
	source, err := os.ReadFile("gui_windows.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(source), "return guiSaveConfig(dir, cfg)") {
		t.Error("saveConfig binding must return guiSaveConfig's structured result")
	}
}
