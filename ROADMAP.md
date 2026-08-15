# Roadmap

> 定位一句话：**AI 生成或手写的本地 Web 服务，双击变成桌面应用——零工具链、秒级打包、跑不起来还能自动修。**
>
> DeskWrap 不跟 AI agent（opencode / claude-code 类）竞争：它们负责**生成代码**，DeskWrap 负责**落地**（装依赖 → 跑起来 → 包成桌面应用）。生态里现有 wrapper（Pake/nativefier/ToDesktop）都假设服务"已经能跑"且面向静态成品；DeskWrap 补的是"让项目先跑起来"的最后一公里。

## 已完成（2.0）

- [x] 单 Go 可执行文件（~8.5MB）+ 系统 WebView2，打包产物 5-8MB zip、秒级完成
- [x] CLI（init/run/build/detect/gui/help）+ 7 面板管理 GUI
- [x] 项目自动检测（Node/Vite/Next、Python/Flask/FastAPI/Gradio/Streamlit、Go、pnpm workspace）
- [x] 进程树清理（taskkill /T /F）、防黑窗、托盘、自动重启、单实例
- [x] AI 诊断（24 家提供商）+ 智能体自动修复循环（装依赖/改配置/换端口，15 轮）
- [x] 开源平台搜索 + 一键克隆（GitHub/Gitee/HF/魔搭/GitLab/Codeberg）
- [x] 中英双语、UTF-8 控制台、GBK/ACP 日志解码
- [x] 纯 GUI 版发布文件夹（release/DeskWrap-GUI/）

## 近期（护城河：检测 + 进程管理 + AI 修复闭环）

- [ ] **检测覆盖扩展**：Rust(axum/actix)、Go(gin/echo)、PHP、Ruby、Django 端口解析细化；`uv`/`poetry`/`pipenv` 项目识别
- [ ] **AI 修复闭环增强**：agent 记忆（本次会话修过的坑）、危险命令白名单化、修复步骤可回滚（配置 diff）
- [ ] **进程树治理升级**：孤儿进程检测、端口占用检测（netstat 语义化）、崩溃 dump 收集
- [ ] **日志体验**：日志级别着色、服务输出实时流式到 GUI（当前为刷新式）
- [ ] 一键部署增强：克隆后自动 init + 依赖安装引导（衔接 agent）

## 中期（差异化：与 AI agent 生态互补）

- [ ] **Agent 集成接口**：MCP server / CLI 一键命令，让 opencode、claude-code、DeepSeek Harness 等生成代码后直接调用 `deskwrap run`——"生成→落地"闭环
- [ ] **项目模板化**：GUI 内置常用模板（Flask/Vite/Gradio 骨架）一键生成 + 直接运行
- [ ] **打包增强**：图标配置化（config 指定 icon）、版本信息注入、单文件 zip 自解压
- [ ] **开机自启/服务模式**：托盘菜单注册自启、无窗口服务模式
- [ ] 多实例管理：GUI 同时管理多个已打包应用（启动/停止/卸载）

## 远期

- [ ] macOS / Linux 原生窗口（当前非 Windows 为浏览器回退；候选：Wails 式系统 webview 绑定）
- [ ] 应用商店式"开源项目桌面化"目录（一键体验任意开源 Web 项目）
- [ ] 团队共享：配置云同步/导入导出（密钥仅本机）

## 设计约束（勿破坏）

- 打包产物 = exe + 配置 + WebView2Loader.dll，绝不回退到捆绑 electron-dist/node
- 服务 spawn 走 cmd shim 直启，禁止 shell 拼接（空格路径 + 黑窗双坑）
- 输出解码 UTF-8→系统 ACP 动态转换（勿硬编码 GBK）
- 代理只作用于 DeskWrap 自身，不注入服务 env
- 界面/日志仅中英双语；API key 不落仓库
