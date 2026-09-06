# dsh-desktop — DeepSeek Harness 桌面壳（Wails v3）

把 dsh（`dsh --profile web`）装进一个原生 macOS 桌面窗口：自动启动子进程、监控其输出 URL、把窗口切换到 dsh 界面，并内置 dsh 的自动更新管理。

> **为什么用 Wails v3 而不是 v2**：v2 的 WKWebView 从 `wails://` 自定义 scheme 跳到 `http://` 时**不保存 303 的 Set-Cookie**，dsh 的标准 token 认证（303+Set-Cookie）会失败、前端永远 401。v3 的 `WebviewWindowOptions.URL` 支持直接加载外部 `http://` URL，认证在 http:// 域内正常完成。详见 [POC 结论](../dsh-desktop-poc/README.md)。

## 架构

```
┌──────────────────────────────────────────────┐
│ Wails v3 桌面壳 (Go)                           │
│  ┌────────────────────────────────────────┐  │
│  │ ShellService (Go bind，前端可调用)        │  │
│  │  GetStatus / StartDsh / RestartDsh     │  │
│  │  CheckUpdate / InstallUpdate           │  │
│  │  GetConfig / SaveConfig / OpenInBrowser│  │
│  └──────────────────┬─────────────────────┘  │
│                     │ ServiceStartup/Shutdown │
│  ┌──────────────────▼─────────────────────┐  │
│  │ dshproc.Manager                         │  │
│  │  spawn `dsh --profile web --no-open`    │  │
│  │  └─ 监控 stdout，解析 "dsh web: http://  │  │
│  │     .../?token=..." → onURL             │  │
│  │  └─ 崩溃看护 / Restart / Cleanup        │  │
│  └──────────────────┬─────────────────────┘  │
│  ┌──────────────────▼─────────────────────┐  │
│  │ updater.Updater                         │  │
│  │  Check  : npm dist-tags + semver 比较   │  │
│  │  Install : npm -g 更新 + pnpm update    │  │
│  └────────────────────────────────────────┘  │
└──────────────────┬───────────────────────────┘
                   │ window.SetURL(url)（dsh 就绪后切换）
   ┌───────────────▼───────────────────────────┐
   │ 主窗口：splash → dsh web (http://127.0.0.1)│
   └───────────────────────────────────────────┘
```

## 目录

```
dsh-desktop/
├── main.go                 # 应用入口：窗口 + 图标 + 服务注册
├── services.go             # ShellService（Go bind，前端调用）
├── tray.go                 # 菜单栏/托盘：状态、打开、设置、重启、更新、退出
├── internal/
│   ├── config/             # 配置模型 + JSON 持久化
│   ├── dshproc/            # dsh 子进程管理（spawn/URL 监控/看护）
│   └── updater/            # 自动更新（npm dist-tags + semver + pnpm）
├── frontend/
│   ├── index.html          # splash / 设置面板
│   └── bindings/           # `wails3 generate bindings` 产物（勿手编）
├── build/
│   ├── appicon.png         # 应用图标（来自 dsh-shell.png）
│   ├── tray.png            # 托盘小图标（22×22）
│   ├── darwin/icon.icns    # macOS 打包图标（`wails3 generate icons`）
│   └── windows/icon.ico    # Windows 打包图标
└── go.mod / go.sum
```

## 功能

- **图标**：`build/appicon.png` 嵌入为应用/Dock 图标（`Options.Icon`）与托盘图标；`.icns`/`.ico` 供打包使用。
- **托盘**：菜单栏状态项，显示 dsh 运行状态/PID，支持 打开界面、设置面板、重启 dsh、检查/安装更新、开机自启勾选、退出；点击托盘图标切换窗口；**关闭主窗口隐藏到托盘**（dsh 持续在后台服务）。
- **关闭不退出 / 启动最大化（设置开关，默认开启）**：主窗口被关闭（标题栏 ✕ / Cmd+W）时默认隐藏到托盘继续后台运行，点 Dock 图标可恢复；也可在设置里关闭（关闭即退出应用）。"启动最大化"让主窗口启动时 Zoom 填满屏幕。前者即时生效，后者下次启动生效（见设置页提示）。
- **崩溃自愈**：dsh 子进程异常退出（崩溃/被杀）时自动重启（2s 退避）；用户主动停止/退出时不重启。
- **平滑重启（窗口不关）**：重启 dsh（托盘/设置面板"重启 dsh"、更新内核后、改端口、崩溃自愈）时窗口全程保持打开。若窗口正显示 dsh 页面，会先注入全屏"dsh 正在重启…"遮罩（避免看到断连白屏/浏览器错误页），dsh 就绪后自动移除遮罩并恢复：URL 相同则 `reload`、URL 变化（token 变了）则自动 `location.replace` 导航。
- **端口冲突处理**：启动时检测首选端口是否被占用（如浏览器 GUI、其他 dsh），占用则自动选下一个可用端口（`resolvePort`），与现有实例**共存**；空闲则按配置接管。
- **单实例锁**：同一时间只允许一个 DshShell（`Options.SingleInstance`）。再次启动（双击/Dock/深链接）不另开进程，而是把已运行窗口带回前台。
- **开机自启**：托盘"开机自启"勾选项，注册为登录启动项（SMAppService）。
- **深链接 `dsh://`**：注册 `dsh` URL scheme。外部（VS Code 扩展、`open dsh://…`）可用 `dsh://session/<id>`、`dsh://workspace/<id>/session/<id>`、`dsh://?session=…` 唤起桌面壳并直达 dsh web 对应会话（dsh web 侧由 `dsh-deeplink` 插件的 `?session=`/`?workspace=` 参数支持）。已运行实例通过 kAEGetURL 收到；未运行则从第二实例参数转发。
- **同步 URL 到 VS Code（设置开关，默认关）**：dsh 就绪时把当前 URL（含 token 则带 token）写进 VS Code 用户设置 `dshSessions.baseUrl`（dsh-sessions 扩展会自动热更新侧栏，免去每次重启手动粘贴）。仅在**装了该扩展 + 设置里已存在该键**时才写；settings.json 按注释保留逐字节修改并原子写回。
- **设置面板（可编辑）**：托盘"设置面板"进入 splash（`/?settings=1`）；除状态/操作外，可配置 **端口 / DSH_HOME / dsh 路径 / 更新通道 / 自动检查** 并保存：端口或路径变化会平滑重启 dsh（窗口不关），通道变化立即按新通道检查。
- **启动兼容（GUI/launchd）**：`augmentPath()` 补全 PATH（GUI 启动不继承 shell PATH）；`cmd.Dir` 清理被 LaunchServices 打乱的 PWD；`pty.Start` 给 dsh 分配伪终端（无 TTY 的 launchd 场景 dsh 会卡死）。
- **主窗口**：splash 启动 → 解析 dsh URL 后**页面脚本 `location.replace` 导航**到 dsh（v3 运行时 SetURL 无法可靠跨到 http origin）；Go 侧 ExecJS 立即导航 + 1.5s 兜底（兜底只在未到达时补一次，不会二次刷新）。
- **更新完成提示**：安装更新完成后弹原生对话框（成功：版本 + 已平滑重启；失败：错误信息）。
- **设置面板**：托盘"设置面板"进入 splash（`/?settings=1`），可停留查看状态/触发操作（首次启动才自动跳 dsh）。
- **诊断日志**：`~/Library/Logs/dsh-desktop.log` 记录启动全流程（open 启动也能排查）。
- **自动更新**：见下文。

## 打包（.app）

```bash
# 生产构建（strip + production tag）
go build -tags production -trimpath -buildvcs=false -ldflags="-w -s" -o bin/dsh-desktop .

# 组装 .app（已生成 build/darwin/Info.plist + icon.icns）
APP="bin/dsh-desktop.app"
rm -rf "$APP"; mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
cp bin/dsh-desktop "$APP/Contents/MacOS/"
cp build/darwin/icon.icns "$APP/Contents/Resources/icon.icns"
cp build/darwin/Info.plist "$APP/Contents/"
codesign --force --deep --sign - "$APP"

# 运行
open "bin/dsh-desktop.app"
```

产物：`bin/dsh-desktop.app`（ad-hoc 签名，开发用；正式分发需 Apple 开发者签名 + 公证）。

## 构建与运行

```bash
# 构建 wails3 CLI（生成 bindings 用；首次）
go build -o /tmp/wails3 github.com/wailsapp/wails/v3/cmd/wails3

# 生成 bindings（改动 Go bind 方法后）
/tmp/wails3 generate bindings

# 构建
go build -o dsh-desktop .

# 运行（默认用 ~/.dsh；配置在 ~/Library/Application Support/dsh-desktop/config.json）
./dsh-desktop
```

测试配置示例（用干净 DSH_HOME + 自定义端口）：
```json
{
  "port": 3377,
  "autoUpdate": false,
  "homeDir": "/tmp/dsh-test-home",
  "dshBin": "/Users/<你>/.local/bin/dsh"
}
```

## 自动更新设计

- **通道**：`alpha`（默认，跟踪 dist-tags 最高版本，适合预发布用户）或 `stable`（跟踪 `latest` tag）。
- **版本比较**：语义化版本比较（`semverGt`），正确处理 `0.1.2-alpha.4` / `0.1.1-rc.2` 这类预发布版本，避免"旧版当新版"误报。
- **检查**：`npm view @deepseek-ai/dsh dist-tags --json` → 选出通道内最高版本 → 与 `dsh --version` 比较。
- **安装**：`npm install -g @deepseek-ai/dsh@latest`（内核）→ 在 `$DSH_HOME/profiles/web` 执行 `pnpm update`（插件）→ 重启 dsh 子进程让新版本生效。
- **检查时机**：`config.autoUpdate` 开启时启动先检查一次，之后**按 `updateIntervalHours`（默认 6 小时，设置面板可配）周期自动检查**；也可托盘/设置面板随时手动"检查更新"。
- **通道可配置**：设置面板选择 alpha/stable 并保存，立即按新通道重新检查。

## 验证

```bash
go test ./...       # 单元测试（dshproc URL/生命周期/自动重启、updater semver、deeplink/导航脚本）
go vet ./...
```

## 分发与公证

- 日常安装用 `./scripts/build-and-install.sh`（ad-hoc 签名，仅本机）。
- 分发给其他 Mac 需要 Apple Developer ID 签名 + 公证：提供 `APPLE_ID` / `APPLE_TEAM_ID` / `DEVELOPER_CERT` 环境变量后运行
  `./scripts/sign-and-notarize.sh`（Developer ID 签名 → notarytool 公证 → stapler 盖章 → 装到 /Applications）。

## 后续待办（尚未实现）

- [ ] 托盘"安装更新"进度条（当前：托盘触发时若不在设置面板，完成/失败会弹原生对话框提示）
- [ ] dsh-desktop 自身自动更新（当前只更新 dsh 内核+插件，不含壳本体的静默更新与重启切换）
- [ ] 正式签名 + 公证的 CI 化（脚本已提供：`scripts/sign-and-notarize.sh`，需 Apple 开发者账号凭据）
