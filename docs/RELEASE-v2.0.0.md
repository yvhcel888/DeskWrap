# DeskWrap v2.0.0

首个 Go 重写版正式发布 / First Go-rewrite release.

> Wrap any local web service into a native desktop app — one config, one icon, zero browser tabs.
> 把任意本地 Web 服务一键打包成原生桌面应用。

## 亮点 / Highlights

- **单 Go 可执行文件 (~8.5MB) + 系统 WebView2**：不捆绑 Electron，Win11 自带运行时，缺失自动静默安装
- **打包产物双击即用**：`build` 自动捆绑项目依赖（node_modules）+ 对应版本 Node.js，对方解压双击即可，**无需安装 Node/Python/浏览器**
- **轻量产物**：打包体积只有源码 + 依赖（无 Electron 250MB 冗余）；纯工具本体 zip 仅 ~3.6MB
- **零工具链**：单 exe 即出包，无需 Rust/Cargo/Node
- **自动检测**：Vite/Next.js/Flask/FastAPI/Gradio/Streamlit/Go/pnpm workspace 的启动命令与端口
- **进程树治理**：关窗即清服务进程树（taskkill /T /F），cmd shim 直启防黑窗
- **去密打包**：产物不包含你的 deskwrap.config.json（可能含 API Key）、.env、代理与 AI 配置
- **AI 日志诊断**：粘贴报错日志，AI 给出修复建议（24 家提供商，需自备 API Key）
- **开源项目一键体验**：GitHub/Gitee/HuggingFace/魔搭/GitLab/Codeberg 搜索 → 克隆 → 跑起来 → 打包
- **7 面板管理 GUI**（项目/环境/代理/AI/部署/日志诊断/设置）+ CLI（init/run/build/detect/gui/help）
- 托盘、自动重启、单实例、中英双语、UTF-8 控制台、GBK/ACP 日志解码

## 下载 / Downloads

| 产物 | 说明 |
| --- | --- |
| `DeskWrap-GUI-2.0.0-win-x64.zip` | **纯 GUI 版**：解压双击 DeskWrap-GUI.exe 即用，无需命令行 |
| `deskwrap-2.0.0-win-x64.zip` | 完整版（CLI + GUI）：deskwrap.exe |
| `SHA256SUMS.txt` | 校验和 |

> 系统要求：Windows 10/11（WebView2 Runtime；Win11 自带，缺失时程序自动静默安装）。
> 打包时本机需要 Node.js（用于捆绑运行时）；**打包产物运行时无需任何环境**。

## 变更记录 / Changelog

见 [CHANGELOG.md](CHANGELOG.md)（2.0.0 = Go 重写版；1.x 为已归档的 Electron 旧版）。

## 反馈 / Feedback

- Issues: https://github.com/yvhcel888/DeskWrap/issues
- 欢迎扔项目来测：选一个自己的 Web 项目 → GUI「项目」面板 → 自动检测 → 运行/打包，坏得越离谱越好 😄
