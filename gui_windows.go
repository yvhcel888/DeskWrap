//go:build windows

// DeskWrap - native windows via the system WebView2 runtime (pure Go, no
// cgo). One window wraps the user's service; the other is the management
// GUI (7 panels). The gui.html/error.html assets are embedded into the
// binary, so the whole app ships as exe + WebView2Loader.dll + config.
package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"github.com/getlantern/systray"
	webview2 "github.com/jchv/go-webview2"
	"golang.org/x/sys/windows/registry"
)

//go:embed assets/gui.html
var guiHTML string

//go:embed assets/error.html
var errorHTML string

//go:embed assets/tray.png
var trayIcon []byte

var (
	guiWin     webview2.WebView
	serviceWin webview2.WebView
	winMu      sync.Mutex

	appDone     = make(chan struct{})
	appDoneOnce sync.Once

	showSignalCh = make(chan struct{}, 8)

	closeToTray atomic.Bool
	trayQuit    atomic.Bool

	// subclassing for close-to-tray
	subclassedHWND uintptr
	origWndProc    uintptr
)

func windowsDone() {
	appDoneOnce.Do(func() { close(appDone) })
}

// --- platform init: console hiding + single instance ------------------------

var (
	kernel32dll = syscall.NewLazyDLL("kernel32.dll")
	user32dll   = syscall.NewLazyDLL("user32.dll")

	procCreateMutexW          = kernel32dll.NewProc("CreateMutexW")
	procCreateEventW          = kernel32dll.NewProc("CreateEventW")
	procOpenEventW            = kernel32dll.NewProc("OpenEventW")
	procSetEvent              = kernel32dll.NewProc("SetEvent")
	procWaitForSingleObject   = kernel32dll.NewProc("WaitForSingleObject")
	procGetConsoleProcessList = kernel32dll.NewProc("GetConsoleProcessList")
	procGetConsoleWindow      = kernel32dll.NewProc("GetConsoleWindow")
	procCloseHandle           = kernel32dll.NewProc("CloseHandle")

	procShowWindow          = user32dll.NewProc("ShowWindow")
	procSetForegroundWindow = user32dll.NewProc("SetForegroundWindow")
	procPostQuitMessage     = user32dll.NewProc("PostQuitMessage")
	procSetWindowLongPtrW   = user32dll.NewProc("SetWindowLongPtrW")
	procCallWindowProcW     = user32dll.NewProc("CallWindowProcW")
	procDefWindowProcW      = user32dll.NewProc("DefWindowProcW")
)

// hideConsoleIfDetached hides our console when the app was double-clicked
// (fresh console, only our process attached). When launched from a terminal
// the console is shared (>=2 attached processes) and stays visible so CLI
// output and service logs remain readable.
func hideConsoleIfDetached() {
	if devFlag || hasFlag("--show-console") {
		return
	}
	list := make([]uint32, 8)
	n, _, _ := procGetConsoleProcessList.Call(uintptr(unsafe.Pointer(&list[0])), uintptr(len(list)))
	if n > 1 {
		return
	}
	if hwnd, _, _ := procGetConsoleWindow.Call(); hwnd != 0 {
		procShowWindow.Call(hwnd, 0) // SW_HIDE
	}
}

var (
	singleInstanceMutex uintptr
	singleInstanceEvent uintptr
)

// acquireSingleInstance returns false when another DeskWrap instance is
// already running (and signals it to show its window).
func acquireSingleInstance() bool {
	name, _ := syscall.UTF16PtrFromString("DeskWrap_SingleInstance_v2")
	h, _, err := procCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(name)))
	if h == 0 {
		if errno, ok := err.(syscall.Errno); ok && errno == 183 /*ERROR_ALREADY_EXISTS*/ {
			evName, _ := syscall.UTF16PtrFromString("DeskWrap_Show_v2")
			ev, _, _ := procOpenEventW.Call(0x000F0000|0x00100000 /*EVENT_ALL_ACCESS*/, 0, uintptr(unsafe.Pointer(evName)))
			if ev != 0 {
				procSetEvent.Call(ev)
				procCloseHandle.Call(ev)
			}
			return false
		}
		return true // lock failed for another reason - don't block startup
	}
	singleInstanceMutex = h

	evName, _ := syscall.UTF16PtrFromString("DeskWrap_Show_v2")
	ev, _, _ := procCreateEventW.Call(0, 1, 0, uintptr(unsafe.Pointer(evName)))
	singleInstanceEvent = ev
	go func() {
		for {
			procWaitForSingleObject.Call(singleInstanceEvent, 0xFFFFFFFF)
			showSignalCh <- struct{}{}
		}
	}()
	return true
}

func initPlatform() bool {
	hideConsoleIfDetached()
	return acquireSingleInstance()
}

func showWindow(w webview2.WebView) {
	w.Dispatch(func() {
		hwnd := uintptr(w.Window())
		procShowWindow.Call(hwnd, 9) // SW_RESTORE
		procSetForegroundWindow.Call(hwnd)
	})
}

// --- WebView2 runtime detection & auto-install ------------------------------

const wv2ClientKey = `Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}`

func regKeyExists(root registry.Key, path string) bool {
	k, err := registry.OpenKey(root, path, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	_ = k.Close()
	return true
}

func webview2RuntimeAvailable() bool {
	exeDir := filepath.Dir(mustExecutable())
	dll := filepath.Join(exeDir, "WebView2Loader.dll")
	if !fileExists(dll) {
		dll = filepath.Join(mustRepoRoot(), "build", "WebView2Loader.dll")
	}
	if !fileExists(dll) {
		return false
	}
	for _, path := range []string{
		`SOFTWARE\WOW6432Node\` + wv2ClientKey,
		`SOFTWARE\` + wv2ClientKey,
	} {
		if regKeyExists(registry.LOCAL_MACHINE, path) || regKeyExists(registry.CURRENT_USER, path) {
			return true
		}
	}
	// A recent Edge install also provides the runtime.
	pf := os.Getenv("ProgramFiles(x86)")
	return pf != "" && fileExists(filepath.Join(pf, "Microsoft", "Edge", "Application", "msedge.exe"))
}

// offerWebviewInstall downloads the official Evergreen bootstrapper and
// installs the runtime silently when the user agrees.
func offerWebviewInstall() bool {
	r := msgBox(0,
		"未检测到 WebView2 运行时（微软官方组件），是否自动下载并安装？\n\n"+
			"No WebView2 runtime detected (a Microsoft component).\nDownload and install it automatically?",
		"DeskWrap", 0x4|0x30 /*MB_YESNO|MB_ICONWARNING*/)
	if r != 6 { // IDYES
		openExternal("https://developer.microsoft.com/microsoft-edge/webview2/")
		return false
	}
	tmp := filepath.Join(os.TempDir(), "deskwrap-wv2setup.exe")
	data, _, err := appFetch("GET", "https://go.microsoft.com/fwlink/p/?LinkId=2124703",
		map[string]string{}, nil, 5*time.Minute)
	if err != nil {
		msgBox(0, "下载失败: "+err.Error(), "DeskWrap", 0x10)
		return false
	}
	if err := os.WriteFile(tmp, data, 0o755); err != nil {
		msgBox(0, "写入临时文件失败: "+err.Error(), "DeskWrap", 0x10)
		return false
	}
	defer os.Remove(tmp)
	c := exec.Command(tmp, "/silent", "/install")
	applyHiddenFlags(c)
	done := make(chan struct{})
	go func() { _ = c.Run(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Minute):
		killProcessTree(c.Process.Pid)
	}
	return webview2RuntimeAvailable()
}

// --- window creation ---------------------------------------------------------

// initWindowThread pins the calling goroutine to its OS thread and
// initializes COM as an STA. WebView2 requires the environment to be
// created, messaged and destroyed on one STA thread, so this must run at
// the top of whichever goroutine owns a webview window (the management GUI
// runs on the main goroutine, the service window on a plain goroutine —
// both go through here). Without it the loader's callback reports
// CO_E_NOTINITIALIZED (0x800401F0) and the environment pointer comes back
// NULL. S_OK(0)/S_FALSE(1) both mean the thread is usable.
func initWindowThread() error {
	runtime.LockOSThread()
	hr, _, _ := procCoInitializeEx.Call(0, 2) // COINIT_APARTMENTTHREADED
	if hr != 0 && hr != 1 {
		return fmt.Errorf("CoInitializeEx failed: 0x%x", hr)
	}
	return nil
}

func newWebviewWindow(cfg map[string]any) (webview2.WebView, error) {
	if err := initWindowThread(); err != nil {
		return nil, fmt.Errorf("COM 初始化失败: %w", err)
	}
	if !webview2RuntimeAvailable() {
		if !offerWebviewInstall() {
			return nil, fmt.Errorf("需要 WebView2 运行时才能显示窗口（WebView2 runtime required）")
		}
		if !webview2RuntimeAvailable() {
			return nil, fmt.Errorf("WebView2 运行时安装失败，请重启后再试")
		}
	}

	width := cfgInt(cfg, "window.width")
	if width <= 0 {
		width = 1280
	}
	height := cfgInt(cfg, "window.height")
	if height <= 0 {
		height = 800
	}
	title := cfgStr(cfg, "appName")
	if title == "" {
		title = "DeskWrap"
	}

	exeDir := filepath.Dir(mustExecutable())
	dataPath := filepath.Join(exeDir, "deskwrap-webview-data")

	w := webview2.NewWithOptions(webview2.WebViewOptions{
		Debug:     devFlag,
		DataPath:  dataPath,
		AutoFocus: true,
		WindowOptions: webview2.WindowOptions{
			Title:  title,
			Width:  uint(width),
			Height: uint(height),
			Center: true,
		},
	})
	if w == nil {
		return nil, fmt.Errorf("无法初始化 WebView2（运行时缺失或损坏）")
	}

	minW := cfgInt(cfg, "window.minWidth")
	minH := cfgInt(cfg, "window.minHeight")
	if minW > 0 && minH > 0 {
		w.SetSize(minW, minH, webview2.HintMin)
	}
	return w, nil
}

// --- service window (wraps the user's web service) ---------------------------

func createServiceWindow(cfg map[string]any) {
	go func() {
		w, err := newWebviewWindow(cfg)
		if err != nil {
			msgBox(0, err.Error(), "DeskWrap", 0x10)
			stopService()
			windowsDone()
			return
		}
		winMu.Lock()
		serviceWin = w
		winMu.Unlock()

		url := fmt.Sprintf("http://127.0.0.1:%d", cfgInt(cfg, "service.port"))
		initParams := map[string]any{
			"url":     url,
			"appName": cfgStr(cfg, "appName"),
		}
		w.Init(fmt.Sprintf("window.__DW_INIT__ = %s;", jsonString(initParams)))
		_ = w.Bind("__dw_retry", func() map[string]any { return guiRetry(cfg) })
		w.Init(`(function(){ window.deskwrap = window.deskwrap || {}; window.deskwrap.retry = function(){ return __dw_retry(); }; })();`)

		// Check if project dependencies are missing (node_modules/ etc.).
		cwd := cfgStr(cfg, "service.cwd")
		if cwd == "" {
			cwd, _ = os.Getwd()
		}
		cmdArr, _ := resolveCommand(pickPath(cfg, "service.command"))
		installCmd := needsDepsInstall(cwd, cmdArr)

		if installCmd != "" {
			// Deps missing: show install page, keep message pump running.
			// runInstallDeps will start the service after installing.
			initParams["depsCmd"] = installCmd
			w.Init(fmt.Sprintf("window.__DW_INIT__ = %s;", jsonString(initParams)))
			_ = w.Bind("__dw_installDeps", func() map[string]any {
				return runInstallDeps(w, cfg, cwd, installCmd)
			})
			w.Init(`(function(){ window.deskwrap.installDeps = function(){ return __dw_installDeps(); }; })();`)
			w.SetHtml(errorHTML)
		} else {
			// Normal: show waiting page, start service poll.
			w.SetHtml(errorHTML)
			go func() {
				timeout := time.Duration(cfgInt(cfg, "service.readyTimeout")) * time.Millisecond
				if timeout <= 0 {
					timeout = 60 * time.Second
				}
				ok := waitForService(cfgInt(cfg, "service.port"), timeout, currentServiceExitCh())
				w.Dispatch(func() {
					if ok {
						w.Navigate(url)
					} else {
						params := map[string]any{"url": url, "appName": cfgStr(cfg, "appName"), "error": "timeout"}
						w.Init(fmt.Sprintf("window.__DW_INIT__ = %s;", jsonString(params)))
						w.SetHtml(errorHTML)
					}
				})
			}()
		}

		// Tray: closing hides the window, the service keeps running.
		if cfgBool(cfg, "tray") {
			closeToTray.Store(true)
			startTray(cfgStr(cfg, "appName"))
			subclassForTray(w)
		}

		// Second instance / tray click -> show.
		go func() {
			for range showSignalCh {
				showWindow(w)
			}
		}()

		w.Run()

		closeToTray.Store(false)
		winMu.Lock()
		serviceWin = nil
		winMu.Unlock()
		if guiWin == nil {
			stopService()
			windowsDone()
		}
	}()
}

// ensureServiceWindow opens (or re-shows) the service window; used by the
// agent's run_service tool and the GUI run button.
func ensureServiceWindow(cfg map[string]any) {
	winMu.Lock()
	w := serviceWin
	winMu.Unlock()
	if w != nil {
		showWindow(w)
		return
	}
	createServiceWindow(cfg)
}

// runInstallDeps runs the auto-detected install command (pnpm install etc.)
// on the webview's goroutine, streams output to the error.html depsBox,
// then starts the service and navigates to it on success.
func runInstallDeps(w webview2.WebView, cfg map[string]any, cwd, installCmd string) map[string]any {
	parts := strings.Fields(installCmd)
	program, cmdArgs := parts[0], parts[1:]

	var c *exec.Cmd
	if runtime.GOOS == "windows" {
		r := resolveWindowsCommand(program, cmdArgs)
		if r.viaShell {
			c = shellCommand(r.program, r.args)
		} else {
			c = exec.Command(r.program, r.args...)
		}
	} else {
		c = exec.Command(program, cmdArgs...)
	}
	c.Dir = cwd
	applyHiddenFlags(c)

	stdout, _ := c.StdoutPipe()
	stderr, _ := c.StderrPipe()
	if err := c.Start(); err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}

	// Stream output to the depsBox div.
	streamOutput := func(r io.ReadCloser) {
		buf := make([]byte, 512)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				text := strings.ReplaceAll(string(buf[:n]), "\\", "\\\\")
				text = strings.ReplaceAll(text, "`", "'")
				text = strings.ReplaceAll(text, "\n", "\\n")
				w.Dispatch(func() {
					w.Eval("document.getElementById('depsBox').textContent += '" + text + "'")
				})
			}
			if err != nil {
				return
			}
		}
	}
	go streamOutput(stdout)
	go streamOutput(stderr)

	_ = c.Wait()
	ok := c.ProcessState != nil && c.ProcessState.Success()
	if !ok {
		return map[string]any{"ok": false, "error": "安装命令退出码非零，请检查上方日志。"}
	}

	// Install succeeded — start the service and wait for the port.
	if err := startService(cfg); err != nil {
		return map[string]any{"ok": false, "error": "依赖安装成功但服务启动失败: " + err.Error()}
	}

	port := cfgInt(cfg, "service.port")
	timeout := time.Duration(cfgInt(cfg, "service.readyTimeout")) * time.Millisecond
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	svcOK := waitForService(port, timeout, currentServiceExitCh())
	if !svcOK {
		return map[string]any{"ok": false, "error": "服务启动超时，请手动重试。"}
	}

	// Navigate to the service.
	url := fmt.Sprintf("http://127.0.0.1:%d", port)
	w.Dispatch(func() { w.Navigate(url) })
	return map[string]any{"ok": true}
}

func guiRetry(cfg map[string]any) map[string]any {
	stopService()
	time.Sleep(500 * time.Millisecond)
	if err := startService(cfg); err != nil {
		return map[string]any{"ok": false}
	}
	port := cfgInt(cfg, "service.port")
	timeout := time.Duration(cfgInt(cfg, "service.readyTimeout")) * time.Millisecond
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	ok := waitForService(port, timeout, currentServiceExitCh())
	if ok && serviceWin != nil {
		url := fmt.Sprintf("http://127.0.0.1:%d", port)
		w := serviceWin
		w.Dispatch(func() { w.Navigate(url) })
	}
	return map[string]any{"ok": ok}
}

// --- tray --------------------------------------------------------------------

func startTray(appName string) {
	go func() {
		defer func() { _ = recover() }()
		systray.Run(func() {
			systray.SetIcon(trayIcon)
			systray.SetTitle(appName)
			systray.SetTooltip(appName)
			mShow := systray.AddMenuItem(t("trayShow"), "")
			mQuit := systray.AddMenuItem(t("trayQuit"), "")
			go func() {
				for {
					select {
					case <-mShow.ClickedCh:
						showSignalCh <- struct{}{}
					case <-mQuit.ClickedCh:
						trayQuit.Store(true)
						winMu.Lock()
						w := serviceWin
						winMu.Unlock()
						if w != nil {
							w.Destroy()
						}
						systray.Quit()
						windowsDone()
						return
					}
				}
			}()
		}, func() {})
	}()
}

// subclassForTray intercepts WM_CLOSE so the window hides instead of dying
// when tray mode is on.
func subclassForTray(w webview2.WebView) {
	w.Dispatch(func() {
		hwnd := uintptr(w.Window())
		subclassedHWND = hwnd
		cb := syscall.NewCallback(trayCloseWndProc)
		old, _, _ := procSetWindowLongPtrW.Call(hwnd, ^uintptr(4-1) /*GWL_WNDPROC = -4*/, cb)
		if old != 0 {
			origWndProc = old
		}
	})
}

func trayCloseWndProc(hwnd, msg, wp, lp uintptr) uintptr {
	if msg == 0x0010 /*WM_CLOSE*/ && closeToTray.Load() && !trayQuit.Load() {
		procShowWindow.Call(hwnd, 0) // SW_HIDE
		return 0
	}
	if origWndProc != 0 {
		ret, _, _ := procCallWindowProcW.Call(origWndProc, hwnd, msg, wp, lp)
		return ret
	}
	ret, _, _ := procDefWindowProcW.Call(hwnd, msg, wp, lp)
	return ret
}

// --- management GUI ----------------------------------------------------------

func createGuiWindow() {
	guiCfg := map[string]any{
		"appName": "DeskWrap",
		"window": map[string]any{
			"width": 900, "height": 680, "minWidth": 720, "minHeight": 560,
		},
	}
	w, err := newWebviewWindow(guiCfg)
	if err != nil {
		msgBox(0, err.Error(), "DeskWrap", 0x10)
		return
	}
	guiWin = w
	bindGuiAPI(w)
	w.SetHtml(guiHTML)

	go func() {
		for range showSignalCh {
			showWindow(w)
		}
	}()

	w.Run()
	guiWin = nil
	quitting.Store(true)
	winMu.Lock()
	sw := serviceWin
	winMu.Unlock()
	if sw != nil {
		sw.Destroy()
	}
	stopService()
	windowsDone()
}

func openExternal(urlStr string) {
	_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", urlStr).Start()
}

// --- GUI API bindings (window.deskwrap.*) ------------------------------------

const jsDeskWrapObject = `(function(){
  var deskwrap = window.deskwrap || {};
  deskwrap.getInfo = __dw_getInfo;
  deskwrap.pickFolder = __dw_pickFolder;
  deskwrap.resolveProject = __dw_resolveProject;
  deskwrap.detect = __dw_detect;
  deskwrap.saveConfig = __dw_saveConfig;
  deskwrap.runApp = __dw_runApp;
  deskwrap.buildApp = __dw_buildApp;
  deskwrap.getLog = __dw_getLog;
  deskwrap.aiDiagnose = __dw_aiDiagnose;
  deskwrap.agentStart = __dw_agentStart;
  deskwrap.agentStop = __dw_agentStop;
  deskwrap.checkEnv = __dw_checkEnv;
  deskwrap.envExtended = __dw_envExtended;
  deskwrap.detectEngines = __dw_detectEngines;
  deskwrap.openExternal = __dw_openExternal;
  deskwrap.aiTest = __dw_aiTest;
  deskwrap.aiProviders = __dw_aiProviders;
  deskwrap.aiModels = __dw_aiModels;
  deskwrap.saveAiToCurrent = __dw_saveAiToCurrent;
  deskwrap.platforms = __dw_platforms;
  deskwrap.platformAdd = __dw_platformAdd;
  deskwrap.platformSearch = __dw_platformSearch;
  deskwrap.platformDeploy = __dw_platformDeploy;
  deskwrap.platformSetToken = __dw_platformSetToken;
  deskwrap.getLocale = __dw_getLocale;
  deskwrap.setLocale = __dw_setLocale;
  deskwrap.onAgentEvent = function(cb){ window.__onAgentEvent = cb; };
  window.__onAgentEvent = window.__onAgentEvent || function(ev){};
  window.deskwrap = deskwrap;
})();`

func bindGuiAPI(w webview2.WebView) {
	fns := map[string]any{
		"__dw_getInfo":        func() map[string]any { return guiGetInfo() },
		"__dw_pickFolder":     func() string { return pickFolderDialog("") },
		"__dw_resolveProject": func(a string) map[string]any { return resolveProjectInput(a) },
		"__dw_detect":         func(dir string) map[string]any { return detect(dir) },
		"__dw_saveConfig": func(dir string, cfg map[string]any) string {
			target := filepath.Join(dir, "deskwrap.config.json")
			_ = writeRawConfig(target, cfg)
			return target
		},
		"__dw_runApp":     func(dir string, cfg map[string]any) map[string]any { return guiRunApp(dir, cfg) },
		"__dw_buildApp":   func(dir string, cfg map[string]any) map[string]any { return guiBuildApp(dir, cfg) },
		"__dw_getLog":     func() []string { return guiGetLog() },
		"__dw_aiDiagnose": func(opts map[string]any) map[string]any { return aiDiagnose(opts) },
		"__dw_agentStart": func(opts map[string]any) map[string]any { return guiAgentStart(opts) },
		"__dw_agentStop": func() map[string]any {
			agentStopped.Store(true)
			return map[string]any{"ok": true}
		},
		"__dw_checkEnv":      func() map[string]any { return checkEnvironment() },
		"__dw_envExtended":   func() map[string]any { return guiEnvExtended() },
		"__dw_detectEngines": func(dir string) map[string]any { return guiDetectEngines(dir) },
		"__dw_openExternal": func(url string) map[string]any {
			if strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://") {
				openExternal(url)
			}
			return map[string]any{"ok": true}
		},
		"__dw_aiTest":           func(opts map[string]any) map[string]any { return aiTest(opts) },
		"__dw_aiProviders":      func() []map[string]any { return guiAiProviders() },
		"__dw_aiModels":         func(opts map[string]any) map[string]any { return aiModels(opts) },
		"__dw_saveAiToCurrent":  func(ai map[string]any) map[string]any { return guiSaveAiToCurrent(ai) },
		"__dw_platforms":        func() map[string]any { return guiPlatforms() },
		"__dw_platformAdd":      func(p map[string]any) map[string]any { return guiPlatformAdd(p) },
		"__dw_platformSearch":   func(id, q string) map[string]any { return platformSearch(id, q) },
		"__dw_platformDeploy":   func(opts map[string]any) map[string]any { return guiPlatformDeploy(opts) },
		"__dw_platformSetToken": func(id, tok string) map[string]any { return guiPlatformSetToken(id, tok) },
		"__dw_getLocale":        func() string { return resolveLocale() },
		"__dw_setLocale":        func(l string) map[string]any { return guiSetLocale(l) },
	}
	for name, f := range fns {
		_ = w.Bind(name, f)
	}
	w.Init(jsDeskWrapObject)
}

// broadcastAgentEvent pushes an agent event into the GUI via the event
// callback registered by gui.html (window.__onAgentEvent).
func broadcastAgentEvent(ev map[string]any) {
	if guiWin == nil {
		return
	}
	js := "window.__onAgentEvent && window.__onAgentEvent(" + jsonString(ev) + ");"
	w := guiWin
	w.Dispatch(func() { w.Eval(js) })
}

// --- handler implementations --------------------------------------------------

func guiGetInfo() map[string]any {
	cfgMu.RLock()
	path := configPath
	appName := cfgStr(config, "appName")
	port := cfgInt(config, "service.port")
	cfgMu.RUnlock()
	var raw map[string]any
	if path != "" {
		if m, err := readRawConfig(path); err == nil {
			raw = m
		}
	}
	return map[string]any{"appName": appName, "port": port, "configPath": path, "config": raw}
}

func guiRunApp(dir string, cfg map[string]any) map[string]any {
	target := filepath.Join(dir, "deskwrap.config.json")
	if err := writeRawConfig(target, cfg); err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	cfg = deepMerge(defaults, cfg)
	if cfgStr(cfg, "service.cwd") == "" {
		svc := cfgMap(cfg, "service")
		svc["cwd"] = dir
	}

	// Environment gate: warn (modal) before starting if the toolchain is
	// missing or the project's engines.node requirement conflicts.
	cmdArr, _ := resolveCommand(pickPath(cfg, "service.command"))
	_, warnings := checkServiceEnvironment(cmdArr, cfgStr(cfg, "service.cwd"))
	if len(warnings) > 0 {
		detail := ""
		for i, w := range warnings {
			if i > 0 {
				detail += "\n\n"
			}
			detail += "• " + w
		}
		r := msgBox(guiHWND(), detail, "DeskWrap - "+t("envCheckTitle"), 0x4|0x30 /*MB_YESNO|MB_ICONWARNING*/)
		if r != 6 {
			return map[string]any{"ok": false, "error": "已取消（环境问题未解决）"}
		}
	}

	cfgMu.Lock()
	config = cfg
	configPath = target
	cfgMu.Unlock()

	if err := startService(cfg); err != nil {
		return map[string]any{"ok": false, "error": "failed to start service: " + err.Error()}
	}
	ensureServiceWindow(cfg)
	return map[string]any{"ok": true}
}

func guiHWND() uintptr {
	if guiWin != nil {
		return uintptr(guiWin.Window())
	}
	return 0
}

func guiBuildApp(dir string, cfg map[string]any) map[string]any {
	res := doBuild(dir, cfg)
	if !res.ok {
		return map[string]any{"ok": false, "error": res.err}
	}
	return map[string]any{"ok": true, "artifacts": res.artifacts}
}

func guiGetLog() []string {
	out := serviceLog.tail(150)
	if b := buildLog.tail(80); len(b) > 0 {
		out = append(out, "--- 构建日志 ---")
		out = append(out, b...)
	}
	return out
}

func guiAgentStart(opts map[string]any) map[string]any {
	ai := aiSettingsFromOpts(opts)
	if enabled, _ := ai["enabled"].(bool); !enabled {
		return map[string]any{"ok": false, "error": "AI 未启用（请先在 AI 设置开启并填 Key）"}
	}
	if ai["apiKey"] == "" && !ai["noKey"].(bool) {
		return map[string]any{"ok": false, "error": "未配置 API Key"}
	}

	goal, _ := opts["goal"].(string)
	if goal == "" {
		goal = "让项目运行成功"
	}
	dir, _ := opts["dir"].(string)
	workDir := dir
	if workDir == "" {
		cfgMu.RLock()
		if configPath != "" {
			workDir = filepath.Dir(configPath)
		}
		cfgMu.RUnlock()
	}

	cfgMu.RLock()
	cmdArr, _ := resolveCommand(pickPath(config, "service.command"))
	port := cfgInt(config, "service.port")
	cfgMu.RUnlock()

	ctx := map[string]any{"dir": workDir, "command": cmdArr, "port": port, "env": checkEnvironment()}

	agentStopped.Store(false)
	return runAgent(agentOpts{
		goal:     goal,
		ai:       ai,
		ctx:      ctx,
		executor: executeAgentTool,
		goalReached: func() bool {
			if regexp.MustCompile(`打包|build`).MatchString(goal) {
				cfgMu.RLock()
				outDir := cfgStr(config, "outDir")
				cfgMu.RUnlock()
				if outDir == "" && workDir != "" {
					outDir = filepath.Join(workDir, "release")
				}
				if dirExists(outDir) {
					for _, f := range safeReaddir(outDir) {
						lf := strings.ToLower(f)
						if strings.HasSuffix(lf, ".exe") || strings.HasSuffix(lf, ".zip") ||
							strings.HasSuffix(lf, ".dmg") || strings.HasSuffix(lf, ".appimage") {
							return true
						}
					}
				}
				return false
			}
			if regexp.MustCompile(`运行|run|启动`).MatchString(goal) {
				return portListening(port)
			}
			return false
		},
		onEvent:  broadcastAgentEvent,
		stopFlag: func() bool { return agentStopped.Load() },
	})
}

func guiEnvExtended() map[string]any {
	return map[string]any{
		"env":            checkEnvironment(),
		"vram":           checkVRAM(),
		"nodeVersions":   detectNodeVersions(),
		"versionManager": detectVersionManager(),
	}
}

func guiDetectEngines(dir string) map[string]any {
	b, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return map[string]any{"required": nil, "ok": true}
	}
	var pkg map[string]any
	if json.Unmarshal(b, &pkg) != nil {
		return map[string]any{"required": nil, "ok": true}
	}
	engines, _ := pkg["engines"].(map[string]any)
	required, _ := engines["node"].(string)
	if required == "" {
		return map[string]any{"required": nil, "ok": true}
	}
	env := checkEnvironment()
	cur := fmt.Sprint(env["node"])
	ok := nodeMajor(cur) >= minNodeMajor(required)
	return map[string]any{"required": required, "current": cur, "ok": ok}
}

func guiAiProviders() []map[string]any {
	out := []map[string]any{}
	for id, p := range aiProviders {
		out = append(out, map[string]any{
			"id": id, "name": p.name, "model": p.model, "base": p.base, "noKey": p.noKey,
		})
	}
	// stable order: sort by id
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && fmt.Sprint(out[j]["id"]) < fmt.Sprint(out[j-1]["id"]); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func guiSaveAiToCurrent(ai map[string]any) map[string]any {
	cfgMu.RLock()
	path := configPath
	cfgMu.RUnlock()
	// No config loaded yet (plain `--gui` launch): persist to the portable
	// app config next to the exe, which is also the first path read on the
	// next start.
	if path == "" {
		path = filepath.Join(filepath.Dir(mustExecutable()), "deskwrap.config.json")
	}
	var current map[string]any
	if fileExists(path) {
		var err error
		current, err = readRawConfig(path)
		if err != nil {
			return map[string]any{"ok": false, "error": err.Error()}
		}
	} else {
		current = map[string]any{}
	}
	curAI := map[string]any{}
	if m, ok := current["ai"].(map[string]any); ok {
		curAI = m
	}
	for k, v := range ai {
		curAI[k] = v
	}
	current["ai"] = curAI
	if err := writeRawConfig(path, current); err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	cfgMu.Lock()
	config = deepMerge(defaults, current)
	configPath = path
	cfgMu.Unlock()
	return map[string]any{"ok": true}
}

func guiPlatforms() map[string]any {
	list := []map[string]any{}
	for _, p := range userPlatforms() {
		list = append(list, map[string]any{
			"id": fmt.Sprint(p["id"]), "name": fmt.Sprint(p["name"]),
			"needsToken": p["needsToken"] == true,
		})
	}
	cfgMu.RLock()
	var customCount int
	if arr, ok := pickPath(config, "platforms").([]any); ok {
		customCount = len(arr)
	}
	cfgMu.RUnlock()
	return map[string]any{"platforms": list, "customCount": customCount}
}

func guiPlatformAdd(plat map[string]any) map[string]any {
	if fmt.Sprint(plat["id"]) == "" || fmt.Sprint(plat["name"]) == "" || fmt.Sprint(plat["search"]) == "" {
		return map[string]any{"ok": false, "error": "平台信息不完整"}
	}
	cfgMu.RLock()
	path := configPath
	cfgMu.RUnlock()
	if path == "" {
		return map[string]any{"ok": false, "error": "尚未选择项目（无配置文件可保存平台）"}
	}
	current, err := readRawConfig(path)
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	arr := []any{}
	if a, ok := current["platforms"].([]any); ok {
		arr = a
	}
	filtered := []any{}
	for _, it := range arr {
		if m, ok := it.(map[string]any); ok && fmt.Sprint(m["id"]) != fmt.Sprint(plat["id"]) {
			filtered = append(filtered, m)
		}
	}
	filtered = append(filtered, plat)
	current["platforms"] = filtered
	if err := writeRawConfig(path, current); err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	cfgMu.Lock()
	config = deepMerge(defaults, current)
	cfgMu.Unlock()
	return map[string]any{"ok": true}
}

func guiPlatformDeploy(opts map[string]any) map[string]any {
	cloneURL := fmt.Sprint(opts["cloneUrl"])
	if cloneURL == "" {
		return map[string]any{"ok": false, "error": "该条目没有可克隆地址"}
	}
	return resolveProjectInput(cloneURL)
}

func guiPlatformSetToken(id, token string) map[string]any {
	cfgMu.RLock()
	path := configPath
	cfgMu.RUnlock()
	if path == "" {
		return map[string]any{"ok": false, "error": "尚未选择项目（无配置文件可保存 Token）"}
	}
	current, err := readRawConfig(path)
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	tokens := map[string]any{}
	if m, ok := current["platformTokens"].(map[string]any); ok {
		tokens = m
	}
	if strings.TrimSpace(token) != "" {
		tokens[id] = strings.TrimSpace(token)
	} else {
		delete(tokens, id)
	}
	current["platformTokens"] = tokens
	if err := writeRawConfig(path, current); err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	cfgMu.Lock()
	config = deepMerge(defaults, current)
	cfgMu.Unlock()
	return map[string]any{"ok": true}
}

func guiSetLocale(locale string) map[string]any {
	value := locale
	if value != "en-US" && value != "zh-CN" {
		value = "auto"
	}
	cfgMu.RLock()
	path := configPath
	cfgMu.RUnlock()
	if path != "" {
		current, err := readRawConfig(path)
		if err == nil {
			current["locale"] = value
			if writeRawConfig(path, current) == nil {
				cfgMu.Lock()
				config = deepMerge(defaults, current)
				cfgMu.Unlock()
			}
		}
	} else {
		cfgMu.Lock()
		config["locale"] = value
		cfgMu.Unlock()
	}
	return map[string]any{"ok": true}
}
