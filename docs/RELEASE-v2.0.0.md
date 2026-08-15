# DeskWrap v2.0.0

首个 Go 重写版正式发布 / First Go-rewrite release.

> Wrap any local web service into a native desktop app — one config, one icon, zero browser tabs.
> 把任意本地 Web 服务一键打包成原生桌面应用。

## 亮点 / Highlights

- **单 Go 可执行文件 (~8.5MB) + 系统 WebView2**：不捆绑 Electron/Node，Win11 自带运行时，缺失自动静默安装
- **秒级打包**：`build` = 复制 exe + 配置 + WebView2Loader.dll → **5–8MB zip**（vs Electron 250MB / Tauri 分钟级编译）
- **零工具链**：单 exe 即出包，无需 Rust/Cargo/Node
- **自动检测**：Vite/Next.js/Flask/FastAPI/Gradio/Streamlit/Go/pnpm workspace 的启动命令与端口
- **进程树治理**：关窗即清服务进程树（taskkill /T /F），cmd shim 直启防黑窗
- **AI 日志诊断（24 家提供商）+ 智能体自动修复**：装依赖、换端口、改配置，最多 15 轮，危险命令拦截
- **开源项目一键体验**：GitHub/Gitee/HuggingFace/魔搭/GitLab/Codeberg 搜索 → 克隆 → 跑起来 → 打包
- **7 面板管理 GUI**（项目/环境/代理/AI/部署/日志诊断/设置）+ CLI（init/run/build/detect/gui/help）
- 托盘、自动重启、单实例、中英双语、UTF-8 控制台、GBK/ACP 日志解码

## 下载 / Downloads

| 产物 | 说明 |
| --- | --- |
| `DeskWrap-GUI-2.0.0-win-x64.zip` | **纯 GUI 版**：解压双击 DeskWrap-GUI.exe 即用，无需命令行 |
| `deskwrap-2.0.0-win-x64.zip` | 完整版（CLI + GUI）：deskwrap.exe |
| `SHA256SUMS.txt` | 校验和 |

> 系统要求：Windows 10/11（WebView2 Runtime；Win11 自带，缺失时程序自动静默安装）。你的服务所需的工具链（Node/Python 等）用你自己的环境，不捆绑。

## 变更记录 / Changelog

见 [CHANGELOG.md](CHANGELOG.md)（2.0.0 = Go 重写版；1.x 为已归档的 Electron 旧版）。

## 反馈 / Feedback

- Issues: https://github.com/yvhcel888/DeskWrap/issues
- 欢迎扔项目来测：选一个自己的 Web 项目 → GUI「项目」面板 → 自动检测 → 运行/打包，坏得越离谱越好 😄
