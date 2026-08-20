<div align="center">

# 🖥️ DeskWrap

**把任意本地 Web 服务一键打包成原生桌面应用——一份配置、一个图标、告别浏览器标签页。**

[English](README.md) | 简体中文 | [日本語](README.ja.md) | [한국어](README.ko.md)

</div>

DeskWrap 是**单个 Go 可执行文件（约 8.5 MB）**，借助系统 [WebView2 运行时](https://developer.microsoft.com/microsoft-edge/webview2/)（Windows 11 自带）把 `localhost` 的 Web 服务变成真正的桌面窗口。不捆绑 Electron，没有 250MB 的臃肿产物。`deskwrap build` 会把项目、依赖和**内置的 Node.js 运行时**一起打包，对方**解压双击即用，无需安装 Node / Python / 浏览器**。

AI 生成的项目缺的"最后一公里"：代码有了，但装依赖、启动、变成双击就能用的应用，还要在终端里折腾。DeskWrap 替你完成——服务起不来时，内置 AI 分析日志、定位原因、给出修复建议。

## 特性

- 🧩 **配置驱动** — 一切都在这一个 `deskwrap.config.json` 里
- 🔗 **路径或仓库地址** — 粘贴本地路径或 git 仓库，自动克隆
- 🔍 **自动检测** — Vite / Next.js / Flask / FastAPI / Gradio / Streamlit / Go / pnpm workspace：启动命令与端口自动识别
- 🩺 **就绪探测** — 等端口就绪，超时显示重试页
- 🗑️ **进程树清理** — 关窗即清理整个服务进程树（`taskkill /T /F`，无黑窗）
- 🖥️ **托盘模式** — 关窗后台运行；崩溃自动重启
- 🟢 **环境管理** — 开发/调试用**你自己的**工具链（Node/npm/pnpm/yarn/Git/Python，支持 nvm/volta/fnm）；打包产物自动内置 Node.js
- 🤖 **AI 诊断** — 24 家提供商；分析日志、定位根因、给出修复建议（Key 仅存本地）
- 🌐 **一键开源** — 搜索 GitHub / Gitee / HuggingFace / 魔搭 / GitLab / Codeberg，一键克隆部署
- 📦 **双击即用** — `build` 打包项目 + 依赖 + 内置 Node.js 运行时，对方无需安装任何环境；密钥/配置（deskwrap.config.json、.env、代理/AI 配置）自动剥离
- 🌍 **中英双语界面** — zh-CN / en-US，按系统语言自动切换

## 快速开始

### GUI 版（双击即用，无需命令行）

从 [Releases](https://github.com/yvhcel888/DeskWrap/releases) 下载 `DeskWrap-GUI-2.0.0-win-x64.zip`，解压后双击 **DeskWrap-GUI.exe**：

1. **项目**面板 → 选择文件夹（或粘贴 git 仓库地址）
2. 启动命令与端口自动回填 → 点 **▶ 运行** 打开桌面窗口
3. 或点 **📦 打包** 生成独立应用 zip（含内置 Node.js），对方解压双击即用

### CLI

```bash
deskwrap init ./my-project   # 交互式生成配置
deskwrap run ./my-project    # 桌面窗口运行服务
deskwrap build ./my-project  # 打包（exe + 配置 + app + 内置 Node.js → zip）
deskwrap detect ./my-project # 只看检测结果
deskwrap gui                 # 打开管理 GUI
deskwrap help
```

```jsonc
// deskwrap.config.json
{
  "appName": "My App",
  "service": {
    "command": ["node", "server.js"], // 终端里怎么启动就怎么写
    "cwd": "./my-project",
    "port": 3000                     // 就绪探测端口（0 = 跳过）
  },
  "window": { "width": 1280, "height": 800 },
  "tray": false,
  "autoRestart": false
}
```

### 源码构建

需要 Go 1.25+。

```bash
git clone https://github.com/yvhcel888/DeskWrap.git
cd DeskWrap
go build -ldflags "-s -w" -o deskwrap.exe .
go test ./...   # 单元测试
```

## 为什么选 DeskWrap

| 场景 | 其他工具 | DeskWrap |
| --- | --- | --- |
| AI 生成的项目跑不起来 | 靠 agent / 手动修 | **AI 日志诊断**（定位根因、给出修复建议） |
| 打包体积/时间 | Electron 250MB+ / Tauri 编译分钟级 | **工具本体 3.6MB zip；完整产物 ≈ 项目大小（70–100MB），1–3 分钟**（纯复制，无编译） |
| 工具链要求 | Tauri 要 Rust/Cargo、Electron 要 Node | **对方零工具链**：单 Go exe + 系统 WebView2，Node 已打进包 |
| 面向对象 | 成品网站/静态 URL（Pake、nativefier） | **开发中的服务**——自动检测命令/端口、管进程树 |
| 试用开源项目 | 克隆 → 自己配 → 手动起 | **一键**：搜索 → 克隆 → 检测 → 跑起来 → 打包 |

DeskWrap 与 AI agent（opencode / claude-code 类）是**互补**关系：它们负责**写**代码，DeskWrap 负责**落地**——装、跑、打包。

完整规划见 [ROADMAP.md](ROADMAP.md)。

## 目录结构

```
deskwrap/
├── main.go / cli.go          # CLI 分发（init/run/build/detect/gui）
├── proc_windows.go           # Windows 核心：cmd shim 直启、防黑窗、进程树清理、UTF-8→ACP 解码
├── detect.go                 # 项目自动检测
├── agent.go                  # AI 提供商（日志诊断）
├── gui_windows.go            # WebView2 窗口 + 托盘 + deskwrap.* API 桥
├── assets/gui.html           # 7 面板管理 GUI
├── tools/                    # 开发与诊断工具
└── main_test.go              # 单元测试
```

## Roadmap

见 [ROADMAP.md](ROADMAP.md)。护城河 = **自动检测 + 进程树治理 + AI 日志诊断**，随后是项目模板、macOS/Linux 原生窗口。

## 致谢

[go-webview2](https://github.com/jchv/go-webview2) · [WebView2 运行时](https://developer.microsoft.com/microsoft-edge/webview2/) · [systray](https://github.com/getlantern/systray) · 各开源平台及所有被包装成桌面应用的开源项目。

## 许可

[MIT](LICENSE)
