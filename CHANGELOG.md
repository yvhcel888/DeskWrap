# Changelog

All notable changes to DeskWrap are documented here. The format is based on [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/lang/zh-CN/).

## [2.0.1] - 2026-08-29

2.0.0 发布后的修复与增强：双击即用补全（Node + Python）、pnpm monorepo 支持、多项打包可靠性修复。

### Fixed / 修复

- **zip 不包含运行时（严重）**：`build` 生成的 zip 之前只打包 exe + 配置 + dll + app/，`node/`、`python/` 运行时缺失——对方解压后无法运行。现在 zip 打包整个产物目录，解压双击即用
- **无 Node 环境无法启动（严重）**：`needsRuntimeInstall` 之前用系统 PATH 判断运行时，打包机无 Node 时即使包内已有 `node/node.exe` 也拒绝启动服务。现在打包的运行时视为已提供
- **从其他目录双击报错**：portable 配置的相对 `cwd`（"app"）之前按启动目录解析，从别处双击报 `directory name is invalid` 直接退出。现在相对 exe 所在目录解析
- **API Key 泄漏进打包产物（安全）**：`deskwrap.config.json`（可能含 Key）与 `.env` 之前混入 `app/`；`buildExclude` 只对目录生效，文件未排除。现在文件级排除，portable 配置剥离 ai/proxy/platformTokens/projectsDir/outDir
- **pnpm junction 复制报错**：pnpm 用 Windows junction 链接依赖，Go 的 `Lstat` 将其识别为普通文件，按文件读取报 `ERROR_INVALID_FUNCTION (Incorrect function)`，导致 DeepSeek-Harness 等 monorepo 打包失败。现在自动识别并展开复制
- **循环依赖无限展开**：pnpm 的 A↔B 循环 junction 链导致展开无限递归、产物膨胀数 GB。现在展开深度限制 12 层（运行时 require 缓存 2-3 层即打破循环），zip 跳过超长路径条目
- **`pnpm dsh web` 命令无法改写**：命令改写只支持 `pnpm run <script>`，`pnpm <script> <args>` 形式（如 DSH 的 `pnpm dsh web`）解析失败，产物在对方机器找不到 pnpm。现在支持脚本 + 参数、node/python 开头脚本、tsx/vite/vitest 等 bin 工具解析到真实 JS 入口
- **Vite 8+ 端口探测失败**：Vite 8 默认监听 IPv6 `::1`，用 `127.0.0.1` 探测永远不通。改用 `localhost` 双栈探测
- **打包时 GUI 卡死**：`build` 同步阻塞 WebView2 主线程（拷贝大依赖时界面冻结数分钟）。改为 goroutine 异步执行 + 完成回调

### Added / 新增

- **便携 Python 捆绑**：Python 项目（Gradio/Flask/FastAPI/Streamlit 等）打包时自动创建 venv 并 `pip install -r requirements.txt`，产物内置可移植 Python（自动清理 `__pycache__`），对方无需装 Python、不再看到 "pip install" 提示
- **按项目类型捆绑运行时**：Node 项目捆绑 `node/`，Python 项目捆绑 `python/`，互不冗余
- 单元测试：portable 命令改写三种形态（`pnpm <script> <args>` / vite bin / node 直启）

### Changed / 变更

- 打包产物从"exe + 配置 + dll"升级为"exe + 配置 + dll + app/ + node/ 或 python/"，体积 ≈ 源码 + 依赖（纯工具本体 zip 仍 ~3.6 MB）
- 移除智能体功能（Agent loop），保留 AI 日志诊断
- README 四语言版更新为 v2.0.0 实际能力，移除旧版截图

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
