<div align="center">

# 🖥️ DeskWrap

**로컬 웹 서비스를 네이티브 데스크톱 앱으로 — 설정 파일 하나, 아이콘 하나, 브라우저 탭은 이제 그만.**

[English](README.md) | [简体中文](README.zh-CN.md) | [日本語](README.ja.md) | 한국어

</div>

DeskWrap은 **단일 Go 실행 파일(약 8.5 MB)** 입니다. 시스템의 [WebView2 런타임](https://developer.microsoft.com/microsoft-edge/webview2/)(Windows 11 기본 내장)을 사용해 `localhost` 웹 서비스를 진짜 데스크톱 창으로 바꿔 줍니다. Electron도, Node 번들도 없습니다. `deskwrap build`는 몇 초 만에 끝나고 결과물은 **5–8 MB zip**뿐입니다.

AI가 생성한 프로젝트에 빠져 있던 "마지막 1마일": 코드는 있는데 의존성 설치·실행·더블클릭 앱화에는 여전히 터미널 작업이 필요합니다. DeskWrap이 대신합니다 — 서비스가 안 뜨면 내장 AI가 로그를 분석해 원인을 찾고, 에이전트가 자동으로 고칩니다(의존성 설치, 포트 변경, 설정 조정).

<div align="center">
  <img src="docs/screenshots/panel-project.png" width="480" alt="프로젝트 패널"/>
  <img src="docs/screenshots/panel-log.png" width="480" alt="로그 및 AI 진단 패널"/>
</div>

## 주요 기능

- 🧩 **설정 기반** — 모든 것이 `deskwrap.config.json` 하나에
- 🔗 **경로 또는 git URL** — 로컬 경로나 저장소 URL을 붙여 넣으면 자동 클론
- 🔍 **자동 감지** — Vite / Next.js / Flask / FastAPI / Gradio / Streamlit / Go / pnpm 워크스페이스: 시작 명령과 포트를 자동으로 식별
- 🩺 **준비 상태 프로브** — 포트가 준비될 때까지 대기, 시간 초과 시 재시도 페이지 표시
- 🗑️ **프로세스 트리 정리** — 창을 닫으면 서비스 프로세스 트리 전체 정리(`taskkill /T /F`, 검은 콘솔창 없음)
- 🖥️ **트레이 모드** — 닫아도 백그라운드 실행, 크래시 시 자동 재시작
- 🟢 **환경 관리자** — **내가 설치한** 툴체인 사용(Node/npm/pnpm/yarn/Git/Python, nvm/volta/fnm 지원), 런타임 번들 없음
- 🤖 **AI 진단** — 24개 제공자 지원. 로그를 분석해 원인과 수정 방법 제시(API 키는 로컬에만 저장)
- 🛠️ **에이전트 루프** — "프로젝트를 실행시켜라": 의존성 설치·포트/설정 수정 자동 실행, 최대 15라운드, 위험 명령 차단
- 🌐 **오픈소스 원클릭** — GitHub / Gitee / HuggingFace / ModelScope / GitLab / Codeberg 검색 후 한 번에 배포
- 📦 **초경량 결과물** — `build` = exe + 설정 + WebView2Loader.dll 복사 → zip, 몇 초 만에 완료
- 🌍 **중·영 이중 언어 UI** — zh-CN / en-US, 시스템 언어로 자동 전환

## 빠른 시작

### GUI 버전(더블클릭만으로, 터미널 불필요)

[Releases](https://github.com/yvhcel888/DeskWrap/releases)에서 `DeskWrap-GUI-2.0.0-win-x64.zip`을 받아 압축을 풀고 **DeskWrap-GUI.exe**를 더블클릭하세요:

1. **프로젝트** 패널 → 폴더 선택(또는 git URL 붙여넣기)
2. 시작 명령과 포트가 자동 입력 → **▶ 실행**으로 데스크톱 창 열기
3. 또는 **📦 빌드**로 몇 초 만에 독립 실행 앱 zip 생성

### CLI

```bash
deskwrap init ./my-project   # 설정 대화형 생성
deskwrap run ./my-project    # 데스크톱 창에서 서비스 실행
deskwrap build ./my-project  # 패키징(exe + 설정 + dll → zip)
deskwrap detect ./my-project # 감지 결과 확인
deskwrap gui                 # 관리 GUI 열기
deskwrap help
```

```jsonc
// deskwrap.config.json
{
  "appName": "My App",
  "service": {
    "command": ["node", "server.js"], // 터미널에서 실행하는 그대로
    "cwd": "./my-project",
    "port": 3000                     // 준비 상태 프로브 포트(0 = 건너뜀)
  },
  "window": { "width": 1280, "height": 800 },
  "tray": false,
  "autoRestart": false
}
```

### 소스에서 빌드

Go 1.23+ 필요.

```bash
git clone https://github.com/yvhcel888/DeskWrap.git
cd DeskWrap
go build -ldflags "-s -w" -o deskwrap.exe .
go test ./...   # 유닛 테스트
```

## DeskWrap을 선택하는 이유

| 상황 | 다른 도구 | DeskWrap |
| --- | --- | --- |
| AI 생성 프로젝트가 안 돌아감 | 에이전트에 의존 / 수동 수정 | **AI 로그 진단 + 에이전트 자동 복구**(의존성·포트·설정) |
| 패키지 크기 / 빌드 시간 | Electron 250MB+ / Tauri 수 분 컴파일 | **5–8MB zip, 몇 초** |
| 필요한 툴체인 | Tauri는 Rust/Cargo, Electron은 Node | **제로** — Go exe 하나 + 시스템 WebView2 |
| 대상 | 완성 사이트 / 정적 URL(Pake, nativefier) | **개발 중인 서비스** — 명령/포트 자동 감지, 프로세스 트리 관리 |
| 오픈소스 프로젝트 체험 | 클론 → 직접 설정 → 수동 실행 | **원클릭**: 검색 → 클론 → 감지 → 실행 → 패키징 |

DeskWrap은 AI 에이전트(opencode / claude-code 계열)와 **보완 관계**입니다: 에이전트가 코드를 **쓰고**, DeskWrap이 **착륙**시킵니다(설치·실행·패키징). 이 루프를 잇는 MCP/CLI 브리지를 로드맵에 포함했습니다.

전체 계획은 [ROADMAP.md](ROADMAP.md) 참조.

## 저장소 구성

```
deskwrap/
├── main.go / cli.go          # CLI 디스패치(init/run/build/detect/gui)
├── proc_windows.go           # Windows 코어: cmd shim 직접 실행·블랙윈도우 방지·프로세스 트리 정리·UTF-8→ACP 디코딩
├── detect.go                 # 프로젝트 자동 감지
├── agent.go                  # AI 제공자 + 에이전트 루프
├── gui_windows.go            # WebView2 창 + 트레이 + deskwrap.* API 브리지
├── assets/gui.html           # 7패널 관리 GUI
├── tools/                    # 개발·진단 도구
├── docs/screenshots/         # README용 스크린샷
└── main_test.go              # 유닛 테스트
```

## 로드맵

[ROADMAP.md](ROADMAP.md) 참조. 핵심 경쟁력은 **자동 감지 + 프로세스 트리 관리 + AI 복구 루프**, 이후 에이전트 생태계 연동(MCP/CLI), 템플릿, macOS/Linux 네이티브 창.

## 감사의 말

[go-webview2](https://github.com/jchv/go-webview2) · [WebView2 런타임](https://developer.microsoft.com/microsoft-edge/webview2/) · [systray](https://github.com/getlantern/systray) · 각 오픈소스 플랫폼과 데스크톱 앱으로 포장된 모든 프로젝트에 감사드립니다.

## 라이선스

[MIT](LICENSE)
