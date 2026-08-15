# Changelog

All notable changes to DeskWrap are documented here. The format is based on [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/lang/zh-CN/).

## [2.0.0] - 2026-08-15

Go 重写版 / Go rewrite. 单可执行文件 + WebView2 运行时，彻底告别 Electron。

### Added / 新增

- **Go 单可执行文件**（~8.4 MB）：CLI 与 GUI 合一，无需 Node/npm 安装
- **系统 WebView2 运行时**窗口（Win11 自带；缺失自动下载官方 bootstrapper 静默安装）
- **秒级打包**：`deskwrap build` = 复制 exe + 配置 + WebView2Loader.dll → zip（~5–8 MB，替代旧方案 10 分钟 + 250 MB）
- **cmd shim 三模式直启**（防黑窗、空格路径），Windows 服务 spawn 走原生进程（无 shell 拼接）
- **AI 提供商扩至 24 家**，OpenAI 兼容 baseUrl 支持
- **智能体循环**：6 工具、15 轮上限、危险命令拦截（格式化/删目录）
- **平台部署**：GitHub / Gitee / HuggingFace / 魔搭 / GitLab / Codeberg 搜索 + token + 一键克隆
- **原生文件夹选择器**（IFileOpenDialog，纯 syscall COM，无依赖）
- **托盘 + 单实例锁 + 控制台自动隐藏**
- **中英双语**（GetUserDefaultUILanguage 系统语言检测）
- **环形日志缓冲**（400 行）
- 单元测试：shim 解析 / JSON action 提取 / 配置合并 / 项目检测 / UTF-8 分片解码 / 文件名 sanitize

### Fixed / 修复

- 旧版 Electron 主进程 1426 行 bug 多发：服务 spawn 竞态（retry+autoRestart 双开）、黑窗、空格路径——重写后由 gen 计数防竞态 + shim 直启解决
- 输出解码 UTF-8→系统 ACP 动态转换（注册表读 ACP，不硬编码 GBK）

### Changed / 变更

- **移除 Electron/Node 运行时**（bin/ src/ test/ package.json 全部删除）
- 打包产物从 250 MB 降至 5–8 MB，构建时间从分钟级降至秒级
- 模块名 `github.com/yvhcel888/DeskWrap`，Go 1.23，依赖仅 4 个纯 Go 库（无 cgo）

### Removed / 移除

- 内置 Node.js 便携运行时（改用环境管理器检测用户工具链）

## [1.1.0] - 2026-08-15（旧版 Electron，已归档 / legacy Electron, archived）

### Added / 新增

- 项目地址直接输入（git 仓库自动克隆）
- AI 诊断面板（GLM / DeepSeek / Qwen）
- 代理开关、服务日志缓存、冒烟测试、无配置兜底

### Fixed / 修复

- Electron `path.txt` 换行符导致二进制误下载
- MSYS/git-bash 路径解析
- DeepSeek-Harness 类 pnpm workspace 检测

### Changed / 变更

- 移除内置 Node.js 运行时，改为环境管理器

## [1.0.0] - 2026-08-15（旧版 Electron，已归档 / legacy Electron, archived）

### Added / 新增

- DeskWrap 初版（Electron）：CLI（init/run/build/detect/gui/help）+ 桌面 GUI
- 项目自动检测、端口就绪探测、进程树清理、托盘、自动重启、单实例锁
- electron-builder 打包（Windows 便携版 exe）
