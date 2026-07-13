# Windows 托盘 App Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 给 `bx.exe` 一个图形入口——`bx.exe tray` 启动系统托盘,小白点图标即可连/断/设置/看状态,全程不碰命令行。

**Architecture:** 一个 `bx tray` 子命令(windows-only)启动 `fyne.io/systray` 托盘,**非提权常驻**;它是现有 CLI + Windows 服务 + 控制面之上的**薄 UI 壳**。改动系统的动作(连/断/设置/重启)用 `ShellExecuteW` verb `runas` 拉起**提权** `bx.exe <up|down|setup|restart>` 子进程(仅此时弹 UAC)。状态检测复用 `install.ServiceState`(非管理员只读 SCM)+ config 存在性 + `bx status --json`。

**Tech Stack:** Go 1.26、`fyne.io/systray`、`golang.org/x/sys/windows`(ShellExecute/MessageBox/registry/剪贴板 LazyDLL)、`//go:embed`(.ico)。

## Global Constraints

- Go 1.26;仓库按 **gofumpt** 格式化(`gofumpt -w`,勿 gofmt)。
- 托盘全部 windows-only:UI/syscall 代码在 `//go:build windows` 文件;纯逻辑无 build tag(可 Linux 单测)。`bx tray` 在非 windows 返回清晰错误(`tray_other.go` 桩)。
- **CGO_ENABLED=0 不变**:`fyne.io/systray` 的 windows 实现走 Win32、零 cgo;只在 `//go:build windows` 下 import。
- 提权模型:托盘进程非提权;连/断/设置/重启经 `ShellExecuteW("runas", exe, "<subcmd>")` 提权子进程。改动前 MessageBox 确认(连接/断开/设置都确认)。
- 设置链接来源 = **系统剪贴板**(读剪贴板→校验受支持前缀→提权 `bx setup <link>`)。不做输入对话框。
- 复用不重造:svc 态用 `install.ServiceState("is-active","bx")`;`bx status` 经 spawn `bx.exe status --json` 子进程;链接识别对齐现有 `setup` 支持的前缀。
- 验证命令:`go build ./... && go vet ./... && go test ./...`;交叉编译 `GOOS=windows GOARCH=amd64 go build -o /dev/null ./...`、`GOOS=windows GOARCH=arm64 go build -o /dev/null ./...`;linux/darwin 不受影响(托盘全 windows-tag)。
- 提交信息:中文 conventional commits,结尾 `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`。默认分支直接提交。

## File Structure

- `internal/tray/state.go` (+ `_test.go`) — `TrayState` 枚举、`trayStateFrom`、`parseSetupLink`、`menuItemsFor`(纯逻辑,无 build tag,Linux 可测)。
- `internal/tray/status.go` (+ `_test.go`) — `parseStatusJSON([]byte) (StatusDetail, error)`(纯,TDD)+ `detectState()`(组合 svc 态/config/status,windows-tag 或平台无关用 install.ServiceState 桩)。
- `internal/tray/win_windows.go` — `elevateRun(subcmd string) error`(ShellExecute runas)、`readClipboardText() (string, error)`、`messageBox(title,text string, flags uint32) int`、`confirm(title,text string) bool`、`setAutostart(exePath string) error`、`freeConsole()`。全 Win32,windows-only。
- `internal/tray/tray_windows.go` — `Run() error`:systray.Run(onReady,onExit)、图标/tooltip 轮询、动态菜单 + 各 handler。windows-only。
- `internal/tray/tray_other.go` — `//go:build !windows`:`func Run() error { return errors.New("bx tray 仅支持 Windows") }`。
- `internal/tray/icons/{protected,off,attention}.ico` + `internal/tray/icons_windows.go` — `//go:embed` + `iconFor(state) []byte`。
- `internal/cli/cli.go` — 加 `{Name:"tray", ...Action: trayAction}`;`trayAction` 调 `tray.Run()`。
- `go.mod` — 加 `fyne.io/systray`。

---

### Task 1: 纯托盘逻辑(state/parse/menu,TDD)

**Files:**
- Create: `internal/tray/state.go`
- Test: `internal/tray/state_test.go`

**Interfaces:**
- Produces:
  - `type TrayState int` + 常量 `StateNotInstalled, StateNotSetup, StateOff, StateProtected, StateAttention`.
  - `func trayStateFrom(svcRunning bool, configExists bool, tunnelHealthy bool) TrayState`.
  - `func parseSetupLink(clipboard string) (link string, ok bool)`.
  - `func menuItemsFor(s TrayState) TrayMenu`(`TrayMenu` 含各项 visible/enabled/label 布尔+串)。

- [ ] **Step 1: 写失败测试**

```go
// internal/tray/state_test.go
package tray

import "testing"

func TestTrayStateFrom(t *testing.T) {
	cases := []struct {
		name        string
		svcRunning  bool
		configOK    bool
		healthy     bool
		want        TrayState
	}{
		{"未配置", false, false, false, StateNotSetup},
		{"已配置未跑", false, true, false, StateOff},
		{"跑且健康", true, true, true, StateProtected},
		{"跑但不健康", true, true, false, StateAttention},
	}
	for _, c := range cases {
		if got := trayStateFrom(c.svcRunning, c.configOK, c.healthy); got != c.want {
			t.Errorf("%s: trayStateFrom(%v,%v,%v)=%v want %v", c.name, c.svcRunning, c.configOK, c.healthy, got, c.want)
		}
	}
}

func TestParseSetupLinkAccepts(t *testing.T) {
	for _, in := range []string{
		"bx://abc", "  bx://abc  \n", "vless://x@h:443", "hysteria2://x@h", "brook://x", "blink://x",
	} {
		if link, ok := parseSetupLink(in); !ok || link == "" {
			t.Errorf("应接受 %q", in)
		}
	}
	// trim 生效
	if link, _ := parseSetupLink("  bx://abc  "); link != "bx://abc" {
		t.Errorf("应 trim, got %q", link)
	}
}

func TestParseSetupLinkRejects(t *testing.T) {
	for _, in := range []string{"", "   ", "hello world", "https://x", "not-a-link"} {
		if _, ok := parseSetupLink(in); ok {
			t.Errorf("应拒绝 %q", in)
		}
	}
}

func TestMenuItemsFor(t *testing.T) {
	// 保护中:显示"断开",不显示"连接";显示状态/日志/退出
	m := menuItemsFor(StateProtected)
	if !m.Disconnect.Visible || m.Connect.Visible {
		t.Error("保护中应显示断开、隐藏连接")
	}
	// 已关闭:显示"连接"
	if m := menuItemsFor(StateOff); !m.Connect.Visible || m.Disconnect.Visible {
		t.Error("已关闭应显示连接、隐藏断开")
	}
	// 未配置:显示"从剪贴板设置",连接/断开都隐藏
	if m := menuItemsFor(StateNotSetup); !m.Setup.Visible || m.Connect.Visible || m.Disconnect.Visible {
		t.Error("未配置应只显示设置")
	}
	// 需注意:显示"重启"
	if m := menuItemsFor(StateAttention); !m.Restart.Visible {
		t.Error("需注意应显示重启")
	}
}
```

- [ ] **Step 2: 跑测试看红**

Run: `go test ./internal/tray/ -run 'TrayState|ParseSetupLink|MenuItems' -v`
Expected: FAIL(`undefined: trayStateFrom` 等)

- [ ] **Step 3: 实现**

```go
// internal/tray/state.go
package tray

import "strings"

type TrayState int

const (
	StateNotInstalled TrayState = iota // 服务未注册(理论态;托盘一般已随安装存在)
	StateNotSetup                      // 无 config
	StateOff                           // 有 config,服务未跑
	StateProtected                     // 服务跑且隧道健康
	StateAttention                     // 服务跑但隧道不健康
)

// trayStateFrom 由三个非提权可得的信号合成托盘态。
func trayStateFrom(svcRunning, configExists, tunnelHealthy bool) TrayState {
	if !configExists {
		return StateNotSetup
	}
	if !svcRunning {
		return StateOff
	}
	if tunnelHealthy {
		return StateProtected
	}
	return StateAttention
}

// setupLinkPrefixes 是 bx setup 认的链接前缀(对齐现有 setup/blink 支持)。
var setupLinkPrefixes = []string{"bx://", "blink://", "vless://", "hysteria2://", "hy2://", "trojan://", "ss://", "vmess://", "brook://"}

// parseSetupLink 从剪贴板文本取一条受支持的 setup 链接(trim;校验前缀)。ok=false 表示不是链接。
func parseSetupLink(clipboard string) (string, bool) {
	s := strings.TrimSpace(clipboard)
	for _, p := range setupLinkPrefixes {
		if strings.HasPrefix(strings.ToLower(s), p) {
			return s, true
		}
	}
	return "", false
}

// menuItem 描述一个菜单项的呈现(由态决定)。
type menuItem struct {
	Visible bool
	Label   string
}

// TrayMenu 是按态生成的菜单蓝图(纯数据;windows 侧据此显隐 systray 项)。
type TrayMenu struct {
	Connect    menuItem
	Disconnect menuItem
	Setup      menuItem
	Restart    menuItem
}

func menuItemsFor(s TrayState) TrayMenu {
	m := TrayMenu{
		Connect:    menuItem{Label: "连接"},
		Disconnect: menuItem{Label: "断开"},
		Setup:      menuItem{Label: "从剪贴板设置…"},
		Restart:    menuItem{Label: "重启保护"},
	}
	switch s {
	case StateNotSetup, StateNotInstalled:
		m.Setup.Visible = true
	case StateOff:
		m.Connect.Visible = true
		m.Setup.Visible = true // 允许换链接
	case StateProtected:
		m.Disconnect.Visible = true
	case StateAttention:
		m.Disconnect.Visible = true
		m.Restart.Visible = true
	}
	return m
}
```

- [ ] **Step 4: 跑测试看绿**

Run: `go test ./internal/tray/ -run 'TrayState|ParseSetupLink|MenuItems' -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
gofumpt -w internal/tray/state.go internal/tray/state_test.go
git add internal/tray/state.go internal/tray/state_test.go
git commit -m "feat(tray): 纯托盘逻辑——态映射/链接校验/菜单蓝图(TDD)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: 状态检测(parseStatusJSON 纯 TDD + detectState 组合)

**Files:**
- Create: `internal/tray/status.go`
- Test: `internal/tray/status_test.go`

**Interfaces:**
- Consumes: Task 1 `trayStateFrom`;`install.ServiceState`(既有,非管理员只读 svc 态)。
- Produces:
  - `type StatusDetail struct { Healthy bool; LatencyMS int64; Server string; Transport string }`.
  - `func parseStatusJSON(b []byte) (StatusDetail, bool)`(纯;`bx status --json` 输出→细节;解析失败 ok=false)。
  - `func detectState(exePath, configPath string) (TrayState, StatusDetail)`:svc 态(`install.ServiceState("is-active","bx")=="active"`)+ `configPath` 存在 + spawn `exePath status --json` 解析健康 → 合成态+细节。

- [ ] **Step 1: 写失败测试**(纯 parseStatusJSON;detectState 靠交叉编译+真机)

```go
// internal/tray/status_test.go
package tray

import "testing"

func TestParseStatusJSON(t *testing.T) {
	// bx status --json 的关键字段(对齐 internal/stats/render.go 的 json tag:server/tunnel_healthy/latency_ms/transport)
	b := []byte(`{"server":"1.2.3.4","tunnel_healthy":true,"latency_ms":401,"transport":"reality@1.2.3.4"}`)
	d, ok := parseStatusJSON(b)
	if !ok {
		t.Fatal("应解析成功")
	}
	if !d.Healthy || d.LatencyMS != 401 || d.Server != "1.2.3.4" {
		t.Fatalf("字段错: %+v", d)
	}
}

func TestParseStatusJSONBad(t *testing.T) {
	if _, ok := parseStatusJSON([]byte("not json")); ok {
		t.Error("坏 JSON 应 ok=false")
	}
}
```

> 注:字段名已核实(`internal/stats/render.go`):`server`/`tunnel_healthy`/`latency_ms`/`transport`。

- [ ] **Step 2: 跑测试看红**

Run: `go test ./internal/tray/ -run ParseStatusJSON -v`
Expected: FAIL(`undefined: parseStatusJSON`)

- [ ] **Step 3: 实现**

```go
// internal/tray/status.go
package tray

import (
	"encoding/json"
	"os"
	"os/exec"

	"github.com/getbx/bx/internal/install"
)

type StatusDetail struct {
	Healthy   bool
	LatencyMS int64
	Server    string
	Transport string
}

// parseStatusJSON 解析 `bx status --json` 输出。字段名与 internal/stats/render.go 的 json tag 一致。
func parseStatusJSON(b []byte) (StatusDetail, bool) {
	var raw struct {
		Server        string `json:"server"`
		TunnelHealthy bool   `json:"tunnel_healthy"`
		LatencyMS     int64  `json:"latency_ms"`
		Transport     string `json:"transport"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return StatusDetail{}, false
	}
	return StatusDetail{Healthy: raw.TunnelHealthy, LatencyMS: raw.LatencyMS, Server: raw.Server, Transport: raw.Transport}, true
}

// detectState 非提权合成托盘态 + 细节。svc 态经 install.ServiceState(只读 SCM);健康经 spawn bx status --json。
func detectState(exePath, configPath string) (TrayState, StatusDetail) {
	svcRunning := install.ServiceState("is-active", "bx") == "active"
	_, statErr := os.Stat(configPath)
	configExists := statErr == nil
	var detail StatusDetail
	if out, err := exec.Command(exePath, "status", "--json").Output(); err == nil {
		if d, ok := parseStatusJSON(out); ok {
			detail = d
		}
	}
	return trayStateFrom(svcRunning, configExists, detail.Healthy), detail
}
```

- [ ] **Step 4: 跑测试看绿 + 交叉编译**

Run: `go test ./internal/tray/ -run ParseStatusJSON -v && GOOS=windows GOARCH=amd64 go build ./internal/tray/`
Expected: PASS + windows 编译过(detectState 用 install.ServiceState;linux 下 install.ServiceState 返回非 "active",detectState 仍编译)

- [ ] **Step 5: 提交**

```bash
gofumpt -w internal/tray/status.go internal/tray/status_test.go
git add internal/tray/status.go internal/tray/status_test.go
git commit -m "feat(tray): 状态检测——parseStatusJSON(TDD)+ detectState(svc态/config/status 合成)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Win32 helpers(提权/剪贴板/MessageBox/自启)

**Files:**
- Create: `internal/tray/win_windows.go`(`//go:build windows`)

**Interfaces:**
- Produces(供 tray_windows.go 调用):
  - `func elevateRun(subcmd string) error` — `ShellExecuteW(0,"runas",<自身exe>,subcmd,nil,SW_HIDE)`。
  - `func readClipboardText() (string, error)` — user32 `OpenClipboard/GetClipboardData(CF_UNICODETEXT)` + kernel32 `GlobalLock/GlobalUnlock`(经 `windows.NewLazySystemDLL` 自绑;x/sys 未导出)。
  - `func messageBox(title, text string, flags uint32) int32` — `windows.MessageBox`。
  - `func confirm(title, text string) bool` — `messageBox(..., MB_OKCANCEL|MB_ICONQUESTION) == IDOK`。
  - `func setAutostart(exePath string) error` — `registry.OpenKey(CURRENT_USER, ...\Run, SET_VALUE)` + `SetStringValue("bx", '"'+exePath+'" tray')`。
  - `func freeConsole()` — `windows.FreeConsole()`(隐藏 Explorer 双击时的控制台黑框)。

**实现要点(实现者对照 vendored 源码确认精确签名):**
- `windows.ShellExecute(0, utf16("runas"), utf16(exePath), utf16(subcmd), nil, SW_HIDE)`;`os.Executable()` 取 exePath;subcmd 如 `"up"`/`"down"`/`"restart"`;setup 用 `"setup \""+link+"\""`(注意引号包 link)。ShellExecute 提权后子进程独立;错误(用户拒 UAC → `ERROR_CANCELLED 1223`)返回上层提示。
- 剪贴板:`user32=windows.NewLazySystemDLL("user32.dll")`,procs `OpenClipboard/CloseClipboard/GetClipboardData/IsClipboardFormatAvailable`;`kernel32` 的 `GlobalLock/GlobalUnlock`。`CF_UNICODETEXT=13`。锁定后 `windows.UTF16PtrToString`。全程 defer CloseClipboard。
- `registry` 用 `golang.org/x/sys/windows/registry`:`k,_,err := registry.CreateKey(registry.CURRENT_USER, "Software\\Microsoft\\Windows\\CurrentVersion\\Run", registry.SET_VALUE)`;`k.SetStringValue("bx", `"`+exePath+`" tray`)`;defer k.Close()。
- `windows.MessageBox`:hwnd=0;flags 常量自定义(`MB_OKCANCEL=1, MB_ICONQUESTION=0x20, MB_ICONINFORMATION=0x40, IDOK=1`)。

- [ ] **Step 1: 实现 win_windows.go**(按上述要点;对照 `$(go list -m -f '{{.Dir}}' golang.org/x/sys)/windows` 确认 ShellExecute/MessageBox/registry 精确签名 + 剪贴板 LazyDLL 绑定)

- [ ] **Step 2: 交叉编译验证**

Run: `GOOS=windows GOARCH=amd64 go build ./internal/tray/ && GOOS=windows GOARCH=arm64 go build ./internal/tray/`
Expected: 全过(windows-only 文件,linux 不编)

- [ ] **Step 3: 提交**

```bash
gofumpt -w internal/tray/win_windows.go
git add internal/tray/win_windows.go
git commit -m "feat(tray): Win32 helpers——runas 提权/剪贴板/MessageBox/自启注册

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: 图标 + go.mod 依赖 + `bx tray` 子命令 + tray Run 骨架

**Files:**
- Create: `internal/tray/icons/{protected,off,attention}.ico`、`internal/tray/icons_windows.go`
- Create: `internal/tray/tray_windows.go`(骨架:systray.Run + 轮询设图标/tooltip + Quit)
- Create: `internal/tray/tray_other.go`(`//go:build !windows` 桩)
- Modify: `internal/cli/cli.go`(加 `tray` 命令 + `trayAction`)
- Modify: `go.mod`/`go.sum`(`go get fyne.io/systray`)

**Interfaces:**
- Consumes: Task 1-3(detectState/menuItemsFor)、Task 3(freeConsole)。
- Produces: `tray.Run() error`;`cli` 的 `bx tray`。

- [ ] **Step 1: 取 3 个 .ico**(简单纯色圆点:绿/灰/红,16x16 或 32x32)

用 ImageMagick 或提交预置的小 ico:
```bash
mkdir -p internal/tray/icons
for c in "protected:#2ecc40" "off:#aaaaaa" "attention:#ff4136"; do
  name=${c%%:*}; col=${c##*:}
  convert -size 32x32 xc:none -fill "$col" -draw "circle 16,16 16,6" internal/tray/icons/$name.ico 2>/dev/null || echo "需手工放 $name.ico"
done
ls -la internal/tray/icons/
```
(若无 convert,手工准备 3 个 16/32px 单色 .ico 提交。)

- [ ] **Step 2: icons_windows.go**

```go
//go:build windows

package tray

import _ "embed"

//go:embed icons/protected.ico
var iconProtected []byte

//go:embed icons/off.ico
var iconOff []byte

//go:embed icons/attention.ico
var iconAttention []byte

func iconFor(s TrayState) []byte {
	switch s {
	case StateProtected:
		return iconProtected
	case StateAttention:
		return iconAttention
	default:
		return iconOff
	}
}
```

- [ ] **Step 3: go get systray**

```bash
GOFLAGS=-mod=mod go get fyne.io/systray@latest
```

- [ ] **Step 4: tray_other.go 桩**

```go
//go:build !windows

package tray

import "errors"

// Run 仅 Windows:非 windows 返回清晰错误。
func Run() error { return errors.New("bx tray 仅支持 Windows") }
```

- [ ] **Step 5: tray_windows.go 骨架**(systray.Run + 轮询设图标/tooltip + Quit;完整菜单在 Task 5)

```go
//go:build windows

package tray

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"fyne.io/systray"
)

const configPath = `C:\ProgramData\bx\config.yaml`

// Run 启动托盘(阻塞到退出)。
func Run() error {
	freeConsole() // 隐藏 Explorer 双击时的控制台黑框
	systray.Run(onReady, func() {})
	return nil
}

func onReady() {
	exe, _ := os.Executable()
	systray.SetTitle("bx")
	mQuit := systray.AddMenuItem("退出", "关闭托盘(保护继续运行)")
	go func() {
		for range mQuit.ClickedCh {
			systray.Quit()
			return
		}
	}()
	go pollLoop(exe)
}

// pollLoop 定期刷新图标 + tooltip。
func pollLoop(exe string) {
	for {
		state, detail := detectState(exe, configPath)
		systray.SetIcon(iconFor(state))
		systray.SetTooltip(tooltipFor(state, detail))
		time.Sleep(3 * time.Second)
	}
}

func tooltipFor(s TrayState, d StatusDetail) string {
	switch s {
	case StateProtected:
		return fmt.Sprintf("bx 保护中 · 延迟 %dms · %s", d.LatencyMS, d.Server)
	case StateAttention:
		return "bx 需注意(隧道不健康)"
	case StateOff:
		return "bx 已关闭"
	default:
		return "bx 未配置——复制 bx:// 链接后从菜单设置"
	}
}

var _ = filepath.Dir // 占位,Task 5 用到
```

- [ ] **Step 6: cli.go 加 `bx tray`**

命令表加(在 `run` 附近):
```go
			{Name: "tray", Usage: "启动系统托盘(Windows;点图标连/断/设置/看状态)", Action: trayAction},
```
加 action:
```go
func trayAction(c *cli.Context) error { return tray.Run() }
```
并在 import 加 `"github.com/getbx/bx/internal/tray"`。

- [ ] **Step 7: 全平台构建**

Run: `go build ./... && go vet ./... && go test ./... && GOOS=windows GOARCH=amd64 go build -o /dev/null ./... && GOOS=windows GOARCH=arm64 go build -o /dev/null ./...`
Expected: 全绿(linux 用 tray_other 桩;windows 用 systray)

- [ ] **Step 8: 提交**

```bash
gofumpt -w internal/tray/*.go internal/cli/cli.go
git add internal/tray/ internal/cli/cli.go go.mod go.sum
git commit -m "feat(tray): bx tray 子命令 + systray 骨架(图标/tooltip 轮询)+ 内嵌图标

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: 完整动作菜单(连/断/设置/状态/日志/重启 + 确认 + 自启)

**Files:**
- Modify: `internal/tray/tray_windows.go`(onReady 建全部菜单项 + handler;pollLoop 据态显隐 + 首次注册自启)

**Interfaces:**
- Consumes: Task 1(menuItemsFor)、Task 3(elevateRun/readClipboardText/confirm/messageBox/setAutostart)。

**实现要点:**
- onReady 建全部 systray 菜单项(连接/断开/从剪贴板设置/打开状态/查看日志/重启/退出),各起一个 goroutine 读 `ClickedCh`。
- pollLoop 每轮据 `menuItemsFor(state)` 对各项 `Show()/Hide()`;首轮调 `setAutostart(exe)`(幂等)。
- **连接**:`if confirm("连接","bx 将接管整机流量,继续?"){ elevateRun("up") }`。**断开**:`confirm` → `elevateRun("down")`。**重启**:`confirm` → `elevateRun("restart")`。
- **从剪贴板设置**:`txt,_:=readClipboardText(); link,ok:=parseSetupLink(txt); if !ok { messageBox("设置","请先复制 bx:// 链接,再点此。", MB_ICONINFORMATION); return }; if confirm("设置","用剪贴板里的链接配置 bx?"){ elevateRun("setup \""+link+"\""); }`。
- **打开状态**:`out,_:=exec.Command(exe,"status").CombinedOutput(); messageBox("bx 状态", string(out), MB_ICONINFORMATION)`。
- **查看日志**:`exec.Command("notepad", `C:\ProgramData\bx\service.log`).Start()`。
- elevateRun 出错(用户拒 UAC=1223)时 messageBox 提示或静默,视要点决定(拒 UAC 不弹错更友好)。

- [ ] **Step 1: 实现完整菜单 + handler**(按要点;systray 菜单项 API 对照 vendored `fyne.io/systray` 源码确认 `AddMenuItem`/`Show`/`Hide`/`Disable`/`ClickedCh`)

- [ ] **Step 2: 交叉编译**

Run: `GOOS=windows GOARCH=amd64 go build ./... && GOOS=windows GOARCH=arm64 go build -o /dev/null ./...`
Expected: 全过

- [ ] **Step 3: 提交**

```bash
gofumpt -w internal/tray/tray_windows.go
git add internal/tray/tray_windows.go
git commit -m "feat(tray): 完整动作菜单——连/断/剪贴板设置/状态/日志/重启(确认+提权+自启)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 6: 真机验收(030-SJWJ-GSR-B)

**非 TDD——真机 e2e;失败回对应 Task 修。**

- [ ] **Step 1: 交叉编译 bx.exe**(含托盘)

Run: `GOOS=windows GOARCH=amd64 go build -o bx.exe .`

- [ ] **Step 2: 真机跑托盘全流程**

部署 bx.exe(内嵌全,单文件)→ 非提权 `bx.exe tray`(或双击):
- 托盘出图标(灰/未配置),无控制台黑框(FreeConsole 生效);
- 复制 reality `bx://` → 菜单「从剪贴板设置」→ UAC → setup 成功 → 问「立即连接」→ UAC → `bx up`;
- 图标变绿、tooltip 显延迟/出口;「打开状态」看面板;从别处 `curl http://icanhazip.com` 出口==VPS;
- 「断开」→ UAC → 图标变灰、出口回直连;
- 「退出」关托盘、服务仍跑;重启机器验证托盘自启图标出现。

- [ ] **Step 3: 清理真机**(down/uninstall + 删目录 + 删 HKCU Run 项)。

---

## Self-Review

- **Spec coverage**:提权模型→Task 3(elevateRun)+ Task 5;状态检测(svc/config/socket)→Task 2;5 态映射→Task 1;菜单→Task 1(蓝图)+ Task 5(呈现);剪贴板设置→Task 3(读)+Task 1(校验)+Task 5(接);图标/tooltip→Task 4;自启→Task 3+Task 5;FreeConsole 黑框→Task 4;`bx tray` 入口→Task 4;真机→Task 6。覆盖齐。
- **Placeholder scan**:纯逻辑步含完整代码 + 测试;windows 集成步给结构 + 要点 + "对照 vendored 源码确认精确 API"(systray 菜单项方法、剪贴板 LazyDLL、ShellExecute/MessageBox 签名)——这是 GUI/syscall 集成的合理粒度,非占位(签名已在计划前用 grep 核实存在)。Task 2 注明实现前 grep 确认 stats json 字段真实名。
- **Type consistency**:`TrayState`/`trayStateFrom`/`parseSetupLink`/`menuItemsFor`/`TrayMenu`(Task 1)被 Task 5 用;`detectState`/`StatusDetail`(Task 2)被 Task 4 pollLoop 用;`elevateRun`/`readClipboardText`/`confirm`/`messageBox`/`setAutostart`/`freeConsole`(Task 3)被 Task 4/5 用;`iconFor`(Task 4)被 pollLoop 用——签名一致。
