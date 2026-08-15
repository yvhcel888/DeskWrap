<div align="center">

# 🖥️ DeskWrap

**Wrap any local web service into a native desktop app — one config, one icon, zero browser tabs.**

[简体中文](README.zh-CN.md) | [日本語](README.ja.md) | [한국어](README.ko.md) | English

</div>

DeskWrap is a **single Go executable (~8.5 MB)** that turns a `localhost` web service into a real desktop window using the system [WebView2 Runtime](https://developer.microsoft.com/microsoft-edge/webview2/) (preinstalled on Windows 11). No Electron, no bundled Node, no 250 MB outputs — `deskwrap build` finishes in seconds and produces a **5–8 MB zip**.

The missing last mile for AI-generated projects: the code exists, but installing dependencies, starting it, and turning it into a double-clickable app still takes a terminal dance. DeskWrap does that for you — and when the service won't start, the built-in AI diagnoses the logs and an agent loop fixes it (install deps, change ports, adjust config).

<div align="center">
  <img src="docs/screenshots/panel-project.png" width="480" alt="Project panel"/>
  <img src="docs/screenshots/panel-log.png" width="480" alt="Log & AI diagnosis panel"/>
</div>

## Features

- 🧩 **Config-driven** — everything lives in one `deskwrap.config.json`
- 🔗 **Path or git URL** — paste a local path or a repository; auto-clone
- 🔍 **Auto-detection** — Vite / Next.js / Flask / FastAPI / Gradio / Streamlit / Go / pnpm workspaces: startup command and port detected automatically
- 🩺 **Readiness probe** — waits for the port, shows a retry page on timeout
- 🗑️ **Process-tree cleanup** — closing the window kills the whole service tree (`taskkill /T /F`, no black console windows)
- 🖥️ **Tray mode** — close to tray, service keeps running; auto-restart on crash
- 🟢 **Environment manager** — uses *your* toolchain (Node/npm/pnpm/yarn/Git/Python, incl. nvm/volta/fnm); nothing bundled
- 🤖 **AI diagnosis** — 24 providers; analyzes logs, finds the root cause, suggests fixes (key stored locally only)
- 🛠️ **Agent loop** — "make the project run": installs deps, fixes ports/config, retries up to 15 rounds, dangerous commands blocked
- 🌐 **One-click open source** — search GitHub / Gitee / HuggingFace / ModelScope / GitLab / Codeberg and deploy with a click
- 📦 **Tiny output** — `build` = copy exe + config + WebView2Loader.dll → zip, in seconds
- 🌍 **Bilingual UI** — zh-CN / en-US, auto-detected from the system

## Quick start

### GUI (double-click, no terminal)

Download `DeskWrap-GUI-2.0.0-win-x64.zip` from [Releases](https://github.com/yvhcel888/DeskWrap/releases), extract, double-click **DeskWrap-GUI.exe**:

1. **项目 / Project** → pick a folder or paste a git URL
2. Command & port are auto-filled → **▶ Run** opens the desktop window
3. Or **📦 Build** produces a standalone app zip in seconds

### CLI

```bash
deskwrap init ./my-project   # generate config (interactive)
deskwrap run ./my-project    # run the service in a desktop window
deskwrap build ./my-project  # package (exe + config + dll → zip)
deskwrap detect ./my-project # print detection result
deskwrap gui                 # open the management GUI
deskwrap help
```

```jsonc
// deskwrap.config.json
{
  "appName": "My App",
  "service": {
    "command": ["node", "server.js"], // whatever starts it in your terminal
    "cwd": "./my-project",
    "port": 3000                     // readiness probe (0 = skip)
  },
  "window": { "width": 1280, "height": 800 },
  "tray": false,
  "autoRestart": false
}
```

### Build from source

Requires Go 1.23+.

```bash
git clone https://github.com/yvhcel888/DeskWrap.git
cd DeskWrap
go build -ldflags "-s -w" -o deskwrap.exe .
go test ./...   # unit tests
```

## Why DeskWrap

| Scenario | Other tools | DeskWrap |
| --- | --- | --- |
| AI-generated project won't start | agent / manual fixing | **AI log diagnosis + agent auto-repair** (deps, ports, config) |
| Package size / build time | Electron 250 MB+ / Tauri minutes of compilation | **5–8 MB zip, seconds** |
| Toolchain required | Tauri needs Rust/Cargo, Electron needs Node | **zero** — one Go exe + system WebView2 |
| Target | finished sites / static URLs (Pake, nativefier) | **services in development** — auto-detect command/port, manage process tree |
| Try an open-source project | clone → configure → start manually | **one click**: search → clone → detect → run → package |

DeskWrap is **complementary to AI agents** (opencode / claude-code style): they *write* the code, DeskWrap *lands* it — install, run, package. Roadmap includes an MCP/CLI bridge for that loop.

See [ROADMAP.md](ROADMAP.md) for the full plan.

## Repo layout

```
deskwrap/
├── main.go / cli.go          # CLI dispatch (init/run/build/detect/gui)
├── proc_windows.go           # Windows core: cmd-shim direct launch, no black
│                             # windows, process-tree cleanup, UTF-8→ACP decode
├── detect.go                 # project auto-detection
├── agent.go                  # AI providers + agent loop
├── gui_windows.go            # WebView2 windows + tray + deskwrap.* API bridge
├── assets/gui.html           # 7-panel management GUI
├── tools/                    # dev & diagnostic tools
├── docs/screenshots/         # screenshots used in the READMEs
└── main_test.go              # unit tests
```

## Roadmap

See [ROADMAP.md](ROADMAP.md). The moat: **auto-detection + process-tree governance + AI repair loop**, then agent-ecosystem integration (MCP/CLI), templates, macOS/Linux native windows.

## Acknowledgements

[go-webview2](https://github.com/jchv/go-webview2) · [WebView2 Runtime](https://developer.microsoft.com/microsoft-edge/webview2/) · [systray](https://github.com/getlantern/systray) · the open-source platforms and every project wrapped into a desktop app.

## License

[MIT](LICENSE)
