# Windows 托盘盾牌图标 + 4 态 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 Windows bx 的 exe/托盘图标从裸彩色圆点换成"盾牌+b",并把托盘状态从 3 态扩到 4 态(绿已保护/琥珀有更新/红断网/灰未激活)。

**Architecture:** 图标由一个可复现的 Python 生成器(`winres/gen-icons.py`,Pillow)输出 PNG(exe 图标源)+ 多尺寸 ICO(托盘),`go-winres` 把 PNG 合成进 `rsrc_windows_*.syso`。状态逻辑保持"纯函数可 Linux 单测 + windows-only syscall 薄壳"的既有分层:`trayStateFrom`/`shouldCheckUpdate`/`parseUpdateCheckJSON` 纯逻辑;`tray_windows.go` 轮询循环持节流状态、spawn `bx update --check --json`。

**Tech Stack:** Go 1.26,`fyne.io/systray`(仅 windows-tagged 文件 import),`github.com/tc-hib/go-winres@v0.3.3`,Python3 + Pillow(仅生成期,不进运行时)。

## Global Constraints

- **纯 Windows 改动**:不改 macOS(`apps/macos/**`)、不碰隧道/路由/数据面。
- **配色(verbatim)**:绿 `#22C55E` = `(34,197,94)`;琥珀 `#F5B414` = `(245,180,20)`;红 `#EF4444` = `(239,68,68)`;灰 `#949CA4` = `(148,156,164)`;白 `#FFFFFF`。
- **状态优先级(verbatim,消歧义)**:`!configExists`→灰(NotSetup);`!svcRunning`→灰(Off);`svcRunning && !tunnelHealthy`→红(Attention);`svcRunning && tunnelHealthy && updateAvailable`→琥珀(Warning);否则→绿(Protected)。**故障优先于更新;走备用传输(健康)恒绿。**
- **更新检查节流**:默认间隔 `6h`;失败(网络/MITM)→ `updateAvailable=false`,best-effort,绝不影响绿/红/灰主判定。
- **manifest 不变量**:`winres.json` 的 `execution-level` 恒 `as invoker`;version 占位 `0.0.0.0`(CI 覆盖)。本计划不改 `winres.json`。
- **验证命令**:`go build ./... && go vet ./...`;windows 交叉编译 `GOOS=windows GOARCH=amd64 go build ./...` 与 `GOOS=windows GOARCH=arm64 go build ./...`;`go test ./internal/tray/`。
- **提交信息**:中文 conventional commits,结尾 `Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>`。默认分支直接提交。

---

## File Structure

- `winres/gen-icons.py` — **创建**。图标美术真相源:heater 盾牌+"b",输出所有 PNG/ICO。
- `winres/icon.png` (256²) / `winres/icon16.png` (32²) — **覆盖**。绿盾+b,exe 图标源。
- `internal/tray/icons/protected.ico` / `warning.ico` / `failed.ico` / `off.ico` — **覆盖/创建/重命名**。四态托盘图标(每个含 16/20/24/32)。删除旧 `attention.ico`。
- `rsrc_windows_amd64.syso` / `rsrc_windows_arm64.syso` — **重生成**(`go generate ./...`)。
- `internal/tray/state.go` — **修改**。`StateWarning`、`trayStateFrom` 加 `updateAvailable`、`TrayMenu.Update` + `menuItemsFor`。
- `internal/tray/state_test.go` — **创建/扩展**。状态优先级纯逻辑测试。
- `internal/tray/status.go` — **修改**。`parseUpdateCheckJSON`、`shouldCheckUpdate`、`detectState` 加 `updateAvailable` 入参。
- `internal/tray/status_test.go` — **创建/扩展**。解析 + 节流纯逻辑测试。
- `internal/tray/icons_windows.go` — **修改**。embed `warning.ico`、`attention→failed` 重命名、`iconFor` 四路。
- `internal/tray/tray_windows.go` — **修改**。`pollLoop` 节流更新检查 + 传 `updateAvailable`;"更新到最新版"菜单项;`tooltipFor` warning 态。

---

### Task 1: 盾牌图标资产 + 生成器 + .syso 重生成

**Files:**
- Create: `winres/gen-icons.py`
- Overwrite: `winres/icon.png`, `winres/icon16.png`
- Create: `internal/tray/icons/warning.ico`, `internal/tray/icons/failed.ico`
- Overwrite: `internal/tray/icons/protected.ico`, `internal/tray/icons/off.ico`
- Delete: `internal/tray/icons/attention.ico`
- Regenerate: `rsrc_windows_amd64.syso`, `rsrc_windows_arm64.syso`

**Interfaces:**
- Produces: 四个 ICO 文件名 `protected/warning/failed/off`(Task 4 的 `//go:embed` 依赖这些名字);exe 绿盾图标经 `.syso` 链入。

- [ ] **Step 1: 写图标生成器 `winres/gen-icons.py`**

```python
#!/usr/bin/env python3
"""bx Windows 图标生成器(真相源)。heater 盾牌 + 白色 "b"。
用法: python3 winres/gen-icons.py   # 从仓库根跑
产物: winres/icon.png(256) winres/icon16.png(32)
      internal/tray/icons/{protected,warning,failed,off}.ico(各含 16/20/24/32)
改完重生成 .syso: go generate ./...
"""
import math, os
from PIL import Image, ImageDraw, ImageFont

SS = 8  # 超采样
WHITE = (255, 255, 255, 255)
COLORS = {
    "green": (34, 197, 94, 255),
    "amber": (245, 180, 20, 255),
    "red":   (239, 68, 68, 255),
    "grey":  (148, 156, 164, 255),
}
FONT_CANDIDATES = [
    "/usr/share/fonts/truetype/dejavu/DejaVuSans-Bold.ttf",
    "/usr/share/fonts/truetype/liberation/LiberationSans-Bold.ttf",
]

def _font(px):
    for p in FONT_CANDIDATES:
        if os.path.exists(p):
            return ImageFont.truetype(p, px)
    return ImageFont.load_default()

def _heater_shield(dr, box, fill):
    x0, y0, x1, y1 = box
    w, h = x1 - x0, y1 - y0
    top = y0 + h * 0.06
    r = w * 0.10
    tlx, trx = x0 + w * 0.10, x1 - w * 0.10
    def lerp(a, b, t): return a + (b - a) * t
    steps, curve = 40, []
    for i in range(steps + 1):
        t = i / steps
        if t < 0.45:
            px, py = x1, lerp(top + r, y0 + h * 0.55, t / 0.45)
        else:
            tt = (t - 0.45) / 0.55
            px = lerp(x1, x0 + w * 0.5, tt)
            py = lerp(y0 + h * 0.55, y1, math.sin(tt * math.pi / 2))
        curve.append((px, py))
    left = [(x1 - (px - x0), py) for px, py in reversed(curve)]
    dr.polygon([(trx, top)] + curve + left + [(tlx, top)], fill=fill)
    dr.pieslice([tlx - r, top, tlx + r, top + 2 * r], 180, 270, fill=fill)
    dr.pieslice([trx - r, top, trx + r, top + 2 * r], 270, 360, fill=fill)
    dr.rectangle([tlx, top, trx, top + r], fill=fill)

def render(color, size):
    big = size * SS
    im = Image.new("RGBA", (big, big), (0, 0, 0, 0))
    d = ImageDraw.Draw(im)
    pad = big * 0.08
    _heater_shield(d, (pad, pad, big - pad, big - pad), COLORS[color])
    f = _font(int(big * 0.46))
    tb = d.textbbox((0, 0), "b", font=f)
    tw, th = tb[2] - tb[0], tb[3] - tb[1]
    d.text((big / 2 - tw / 2 - tb[0], big * 0.45 - th / 2 - tb[1]), "b", font=f, fill=WHITE)
    return im.resize((size, size), Image.LANCZOS)

def main():
    root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
    render("green", 256).save(os.path.join(root, "winres", "icon.png"))
    render("green", 32).save(os.path.join(root, "winres", "icon16.png"))
    ico_dir = os.path.join(root, "internal", "tray", "icons")
    sizes = [16, 20, 24, 32]
    for name, color in [("protected", "green"), ("warning", "amber"),
                        ("failed", "red"), ("off", "grey")]:
        base = render(color, 32)
        base.save(os.path.join(ico_dir, name + ".ico"),
                  sizes=[(s, s) for s in sizes])
    print("icons generated")

if __name__ == "__main__":
    main()
```

- [ ] **Step 2: 跑生成器,产出资产**

Run: `python3 winres/gen-icons.py && rm -f internal/tray/icons/attention.ico`
Expected: 打印 `icons generated`;`winres/icon.png` 变绿盾;`internal/tray/icons/` 下有 `protected/warning/failed/off.ico`、无 `attention.ico`。

- [ ] **Step 3: 核对 ICO 尺寸与外观**

Run: `python3 -c "from PIL import Image; [print(n, Image.open('internal/tray/icons/%s.ico'%n).size) for n in ['protected','warning','failed','off']]"`
Expected: 四行,各 `(32, 32)`(ICO 默认打开最大帧;文件内含 16/20/24/32 多帧)。

- [ ] **Step 4: 重生成 .syso**

Run: `go generate ./...`
Expected: 无报错;`git status` 显示 `rsrc_windows_amd64.syso`、`rsrc_windows_arm64.syso` 改动。

- [ ] **Step 5: 验证 exe 链入新图标**

Run:
```bash
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o /tmp/bx-icontest.exe . && \
go run github.com/tc-hib/go-winres@v0.3.3 extract --dir /tmp/bx-ico /tmp/bx-icontest.exe && \
ls /tmp/bx-ico
```
Expected: build 成功;`extract` 抽出 icon 资源(目录含 `.png`/`icon` 项),证明绿盾已链入。

- [ ] **Step 6: Commit**

```bash
git add winres/gen-icons.py winres/icon.png winres/icon16.png \
        internal/tray/icons/ rsrc_windows_amd64.syso rsrc_windows_arm64.syso
git rm --cached internal/tray/icons/attention.ico 2>/dev/null || true
git commit -m "$(printf 'feat(windows): 盾牌+b 图标资产(exe + 四态托盘)\n\n裸圆点换成 heater 盾牌+白色 b;新增 winres/gen-icons.py 作美术真相源\n(Pillow 生成 PNG + 多尺寸 ICO)。托盘四态 protected/warning/failed/off\n对应绿/琥珀/红/灰。重生成 rsrc_windows_*.syso。\n\nCo-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>')"
```

---

### Task 2: 状态模型 — StateWarning + trayStateFrom(4 参)+ 菜单

**Files:**
- Modify: `internal/tray/state.go`
- Modify: `internal/tray/status.go`(detectState 加 `updateAvailable` 入参并传入 `trayStateFrom`)
- Modify: `internal/tray/tray_windows.go:pollLoop`(调用点先传 `false` 占位)
- Test: `internal/tray/state_test.go`

**Interfaces:**
- Produces:
  - `StateWarning TrayState`(新枚举值)
  - `trayStateFrom(svcRunning, configExists, tunnelHealthy, updateAvailable bool) TrayState`
  - `TrayMenu.Update menuItem`;`menuItemsFor(StateWarning)` 令 `Update.Visible=true`、`Disconnect.Visible=true`
  - `detectState(exePath, configPath string, updateAvailable bool) (TrayState, StatusDetail)`(签名新增末参)
- Consumes:(无上游任务)

- [ ] **Step 1: 写失败测试 `internal/tray/state_test.go`**

```go
package tray

import "testing"

func TestTrayStateFromPriority(t *testing.T) {
	cases := []struct {
		name                                       string
		svc, cfg, healthy, update                  bool
		want                                       TrayState
	}{
		{"未配置", false, false, false, false, StateNotSetup},
		{"未配置即便有更新", false, false, false, true, StateNotSetup},
		{"有配置未运行", false, true, false, false, StateOff},
		{"未运行即便有更新", false, true, false, true, StateOff},
		{"运行但不健康", true, true, false, false, StateAttention},
		{"不健康优先于更新", true, true, false, true, StateAttention},
		{"健康且有更新", true, true, true, true, StateWarning},
		{"健康无更新", true, true, true, false, StateProtected},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := trayStateFrom(c.svc, c.cfg, c.healthy, c.update); got != c.want {
				t.Fatalf("trayStateFrom(%v,%v,%v,%v)=%v want %v", c.svc, c.cfg, c.healthy, c.update, got, c.want)
			}
		})
	}
}

func TestMenuItemsForWarningShowsUpdate(t *testing.T) {
	m := menuItemsFor(StateWarning)
	if !m.Update.Visible {
		t.Fatal("StateWarning 应显示 Update 项")
	}
	if !m.Disconnect.Visible {
		t.Fatal("StateWarning 应显示 Disconnect 项")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/tray/ -run 'TrayStateFromPriority|MenuItemsForWarning' -v`
Expected: 编译失败 `undefined: StateWarning` / `trayStateFrom` 参数个数不符 / `m.Update undefined`。

- [ ] **Step 3: 改 `internal/tray/state.go`**

枚举加 `StateWarning`(放在 `StateProtected` 之后,取值稳定即可):

```go
const (
	StateNotInstalled TrayState = iota // 服务未注册(理论态;托盘一般已随安装存在)
	StateNotSetup                      // 无 config
	StateOff                           // 有 config,服务未跑
	StateProtected                     // 服务跑且隧道健康
	StateWarning                       // 服务跑、隧道健康,但有可用更新
	StateAttention                     // 服务跑但隧道不健康
)
```

`trayStateFrom` 加 `updateAvailable` 参数,按 Global Constraints 优先级:

```go
// trayStateFrom 由非提权可得的信号合成托盘态。故障优先于更新;走备用传输(健康)恒绿。
func trayStateFrom(svcRunning, configExists, tunnelHealthy, updateAvailable bool) TrayState {
	if !configExists {
		return StateNotSetup
	}
	if !svcRunning {
		return StateOff
	}
	if !tunnelHealthy {
		return StateAttention
	}
	if updateAvailable {
		return StateWarning
	}
	return StateProtected
}
```

`TrayMenu` 加 `Update` 字段,`menuItemsFor` 默认给 Update 标签、warning 态显示它:

```go
type TrayMenu struct {
	Connect    menuItem
	Disconnect menuItem
	Setup      menuItem
	Restart    menuItem
	Update     menuItem
}

func menuItemsFor(s TrayState) TrayMenu {
	m := TrayMenu{
		Connect:    menuItem{Label: "连接"},
		Disconnect: menuItem{Label: "断开"},
		Setup:      menuItem{Label: "从剪贴板设置…"},
		Restart:    menuItem{Label: "重启保护"},
		Update:     menuItem{Label: "更新到最新版"},
	}
	switch s {
	case StateNotSetup, StateNotInstalled:
		m.Setup.Visible = true
	case StateOff:
		m.Connect.Visible = true
		m.Setup.Visible = true // 允许换链接
	case StateProtected:
		m.Disconnect.Visible = true
	case StateWarning:
		m.Disconnect.Visible = true
		m.Update.Visible = true
	case StateAttention:
		m.Disconnect.Visible = true
		m.Restart.Visible = true
	}
	return m
}
```

- [ ] **Step 4: 改 `internal/tray/status.go` 的 `detectState` 签名 + 调用**

```go
// detectState 非提权合成托盘态 + 细节。updateAvailable 由调用方(轮询循环,节流)传入;
// detectState 自身不发起更新检查。
func detectState(exePath, configPath string, updateAvailable bool) (TrayState, StatusDetail) {
	svcRunning := install.ServiceState("is-active", "bx") == "active"
	_, statErr := os.Stat(configPath)
	configExists := statErr == nil
	var detail StatusDetail
	if out, err := exec.Command(exePath, "status", "--json").Output(); err == nil {
		if d, ok := parseStatusJSON(out); ok {
			detail = d
		}
	}
	return trayStateFrom(svcRunning, configExists, detail.Healthy, updateAvailable), detail
}
```

- [ ] **Step 5: 改 `internal/tray/tray_windows.go` 调用点(占位 false)**

`pollLoop` 内把 `detectState(exe, configPath)` 改成 `detectState(exe, configPath, false)`(Task 5 再接真值):

```go
		state, detail := detectState(exe, configPath, false)
```

- [ ] **Step 6: 跑测试确认通过**

Run: `go test ./internal/tray/ -run 'TrayStateFromPriority|MenuItemsForWarning' -v`
Expected: PASS。

- [ ] **Step 7: windows 交叉编译确认调用点没漏**

Run: `GOOS=windows GOARCH=amd64 go build ./internal/tray/`
Expected: 成功(证明 tray_windows.go 的 detectState 调用已对齐新签名)。

- [ ] **Step 8: Commit**

```bash
git add internal/tray/state.go internal/tray/status.go internal/tray/tray_windows.go internal/tray/state_test.go
git commit -m "$(printf 'feat(tray): StateWarning 态 + trayStateFrom 4 参(更新纳入判定)\n\n新增琥珀 StateWarning(健康+有更新);trayStateFrom 加 updateAvailable,\n优先级:未配/未跑→灰,不健康→红(优先于更新),健康+更新→琥珀,否则绿。\nTrayMenu 加"更新到最新版"项(warning 态可见)。detectState 加 updateAvailable\n入参(调用方传;暂占位 false,下一步接节流检查)。\n\nCo-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>')"
```

---

### Task 3: 更新检查纯逻辑 — parseUpdateCheckJSON + shouldCheckUpdate

**Files:**
- Modify: `internal/tray/status.go`
- Test: `internal/tray/status_test.go`

**Interfaces:**
- Produces:
  - `parseUpdateCheckJSON(b []byte) (available bool, ok bool)` — 解析 `bx update --check --json` 输出;坏 JSON → `(false,false)`。
  - `shouldCheckUpdate(lastChecked, now time.Time, interval time.Duration) bool` — 节流决策。
  - `checkUpdateAvailable(exePath string) bool` — spawn `bx update --check --json` 并解析;任何失败 → `false`。
- Consumes:(无)

- [ ] **Step 1: 写失败测试 `internal/tray/status_test.go`**

```go
package tray

import (
	"testing"
	"time"
)

func TestParseUpdateCheckJSON(t *testing.T) {
	if avail, ok := parseUpdateCheckJSON([]byte(`{"current":"v0.2.7","latest":"v0.3.0","available":true,"verified":true}`)); !ok || !avail {
		t.Fatalf("有更新应 (true,true), got (%v,%v)", avail, ok)
	}
	if avail, ok := parseUpdateCheckJSON([]byte(`{"available":false,"verified":true}`)); !ok || avail {
		t.Fatalf("无更新应 (false,true), got (%v,%v)", avail, ok)
	}
	if _, ok := parseUpdateCheckJSON([]byte(`not json`)); ok {
		t.Fatal("坏 JSON 应 ok=false")
	}
}

func TestShouldCheckUpdate(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	interval := 6 * time.Hour
	if !shouldCheckUpdate(time.Time{}, now, interval) {
		t.Fatal("零值 lastChecked(从未查过)应 true")
	}
	if !shouldCheckUpdate(now.Add(-7*time.Hour), now, interval) {
		t.Fatal("超过间隔应 true")
	}
	if shouldCheckUpdate(now.Add(-1*time.Hour), now, interval) {
		t.Fatal("间隔内应 false")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/tray/ -run 'ParseUpdateCheckJSON|ShouldCheckUpdate' -v`
Expected: 编译失败 `undefined: parseUpdateCheckJSON` / `shouldCheckUpdate`。

- [ ] **Step 3: 在 `internal/tray/status.go` 加三个函数**

顶部 import 增加 `"time"`(`os/exec` 已在)。追加:

```go
// parseUpdateCheckJSON 解析 `bx update --check --json` 输出(字段对齐 internal/cli/update.go
// 的 updateCheckReport)。坏 JSON → ok=false。
func parseUpdateCheckJSON(b []byte) (available bool, ok bool) {
	var raw struct {
		Available bool `json:"available"`
		Verified  bool `json:"verified"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return false, false
	}
	return raw.Available, true
}

// shouldCheckUpdate 报告是否到期该查更新(节流)。lastChecked 零值=从未查过→查。
func shouldCheckUpdate(lastChecked, now time.Time, interval time.Duration) bool {
	if lastChecked.IsZero() {
		return true
	}
	return now.Sub(lastChecked) >= interval
}

// checkUpdateAvailable spawn `bx update --check --json` 判有无更新。非提权只读;
// 任何失败(网络/MITM/坏输出)→ false,绝不连累主状态判定。
func checkUpdateAvailable(exePath string) bool {
	out, err := exec.Command(exePath, "update", "--check", "--json").Output()
	if err != nil {
		return false
	}
	avail, ok := parseUpdateCheckJSON(out)
	return ok && avail
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/tray/ -run 'ParseUpdateCheckJSON|ShouldCheckUpdate' -v`
Expected: PASS。

- [ ] **Step 5: 全包测试 + 交叉编译**

Run: `go test ./internal/tray/ && GOOS=windows GOARCH=amd64 go build ./internal/tray/`
Expected: 全绿。

- [ ] **Step 6: Commit**

```bash
git add internal/tray/status.go internal/tray/status_test.go
git commit -m "$(printf 'feat(tray): 更新检查纯逻辑(解析 + 6h 节流)\n\nparseUpdateCheckJSON 解析 bx update --check --json 的 available;\nshouldCheckUpdate 注入时间做节流决策;checkUpdateAvailable spawn 命令,\n失败 best-effort 返回 false。纯逻辑,Linux 单测覆盖。\n\nCo-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>')"
```

---

### Task 4: icons_windows.go — 四路 iconFor + warning embed

**Files:**
- Modify: `internal/tray/icons_windows.go`

**Interfaces:**
- Consumes: Task 1 的 `internal/tray/icons/{protected,warning,failed,off}.ico`;Task 2 的 `StateWarning`。
- Produces: `iconFor(StateWarning)` → 琥珀字节。

- [ ] **Step 1: 改 `internal/tray/icons_windows.go`**

```go
//go:build windows

package tray

import _ "embed"

//go:embed icons/protected.ico
var iconProtected []byte

//go:embed icons/warning.ico
var iconWarning []byte

//go:embed icons/failed.ico
var iconFailed []byte

//go:embed icons/off.ico
var iconOff []byte

// iconFor 按托盘态选图标字节:protected→绿、warning→琥珀、attention→红(failed),其余→灰(off)。
func iconFor(s TrayState) []byte {
	switch s {
	case StateProtected:
		return iconProtected
	case StateWarning:
		return iconWarning
	case StateAttention:
		return iconFailed
	default:
		return iconOff
	}
}
```

- [ ] **Step 2: windows 交叉编译(amd64 + arm64)确认 embed 命中新文件名**

Run: `GOOS=windows GOARCH=amd64 go build ./internal/tray/ && GOOS=windows GOARCH=arm64 go build ./internal/tray/`
Expected: 均成功(证明 `warning.ico`/`failed.ico` 存在且 embed 路径正确;旧 `attention.ico` 不再被引用)。

- [ ] **Step 3: Commit**

```bash
git add internal/tray/icons_windows.go
git commit -m "$(printf 'feat(tray): iconFor 四路映射(protected/warning/failed/off)\n\nembed warning.ico;attention.ico 重命名为 failed.ico;iconFor 加 StateWarning\n→琥珀分支。\n\nCo-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>')"
```

---

### Task 5: tray_windows.go — 节流更新检查接线 + 更新菜单 + tooltip

**Files:**
- Modify: `internal/tray/tray_windows.go`

**Interfaces:**
- Consumes: Task 2 `detectState(exe, configPath, updateAvailable)`、`StateWarning`、`menuItemsFor.Update`;Task 3 `shouldCheckUpdate`、`checkUpdateAvailable`;既有 `elevateRun(subcmd string)`、`confirm`。
- Produces:(终态,无下游)

- [ ] **Step 1: `onReady` 加"更新到最新版"菜单项 + 点击处理**

在 `mRestart` 之后、`mStatus` 之前加:

```go
	mUpdate := systray.AddMenuItem("更新到最新版", "下载并安装最新 bx")
```

在 restart 的点击 goroutine 之后加一个 update 点击 goroutine:

```go
	go func() {
		for range mUpdate.ClickedCh {
			if confirm("更新 bx", "下载并安装最新版 bx?保护会自动保留。") {
				_ = elevateRun("update")
			}
		}
	}()
```

- [ ] **Step 2: `toggleItems` 加 Update 字段,onReady 传入**

```go
type toggleItems struct {
	Connect    *systray.MenuItem
	Disconnect *systray.MenuItem
	Setup      *systray.MenuItem
	Restart    *systray.MenuItem
	Update     *systray.MenuItem
}
```

`onReady` 末尾 `go pollLoop(...)` 的 `toggleItems{...}` 加 `Update: mUpdate,`:

```go
	go pollLoop(exe, toggleItems{
		Connect:    mConnect,
		Disconnect: mDisconnect,
		Setup:      mSetup,
		Restart:    mRestart,
		Update:     mUpdate,
	})
```

- [ ] **Step 3: `pollLoop` 接入节流更新检查 + 传 updateAvailable + 显隐 Update 项**

```go
// pollLoop 定期刷新图标 + tooltip + 动作项显隐;首轮顺带注册开机自启(幂等,只需一次)。
// 更新检查按 updateCheckInterval 节流,3 秒轮询只用缓存,避免砸更新端点。
func pollLoop(exe string, items toggleItems) {
	var autostartOnce sync.Once
	var lastUpdateCheck time.Time
	var updateAvailable bool
	const updateCheckInterval = 6 * time.Hour
	for {
		if shouldCheckUpdate(lastUpdateCheck, time.Now(), updateCheckInterval) {
			updateAvailable = checkUpdateAvailable(exe)
			lastUpdateCheck = time.Now()
		}

		state, detail := detectState(exe, configPath, updateAvailable)
		systray.SetIcon(iconFor(state))
		systray.SetTooltip(tooltipFor(state, detail))

		m := menuItemsFor(state)
		showOrHide(items.Connect, m.Connect.Visible)
		showOrHide(items.Disconnect, m.Disconnect.Visible)
		showOrHide(items.Setup, m.Setup.Visible)
		showOrHide(items.Restart, m.Restart.Visible)
		showOrHide(items.Update, m.Update.Visible)

		autostartOnce.Do(func() {
			_ = setAutostart(exe)
		})

		time.Sleep(3 * time.Second)
	}
}
```

> 注:`time.Now()` 在轮询循环里可直接用(非纯逻辑;节流的可测部分已由 Task 3 的 `shouldCheckUpdate` 覆盖)。

- [ ] **Step 4: `tooltipFor` 加 StateWarning 态**

```go
// tooltipFor 按态渲染 tooltip 文案。
func tooltipFor(s TrayState, d StatusDetail) string {
	switch s {
	case StateProtected:
		return fmt.Sprintf("bx 保护中 · 延迟 %dms · %s", d.LatencyMS, d.Server)
	case StateWarning:
		return fmt.Sprintf("bx 保护中 · 有新版可用 · 延迟 %dms", d.LatencyMS)
	case StateAttention:
		return "bx 需注意(隧道不健康)"
	case StateOff:
		return "bx 已关闭"
	default:
		return "bx 未配置——复制 bx:// 链接后从菜单设置"
	}
}
```

- [ ] **Step 5: windows 交叉编译(amd64 + arm64)+ vet**

Run: `GOOS=windows GOARCH=amd64 go build ./... && GOOS=windows GOARCH=arm64 go build ./... && GOOS=windows GOARCH=amd64 go vet ./internal/tray/`
Expected: 全部成功。

- [ ] **Step 6: 全量验证**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: 全绿(`internal/tray` 含新状态/更新测试)。

- [ ] **Step 7: Commit**

```bash
git add internal/tray/tray_windows.go
git commit -m "$(printf 'feat(tray): 接入节流更新检查 + 更新菜单 + warning tooltip\n\npollLoop 每 6h 查一次更新(缓存,3s 轮询不重查),把 updateAvailable 传入\ndetectState;新增"更新到最新版"菜单项(warning 态可见,per-action UAC 提权\nbx update);tooltipFor 加 warning 文案。\n\nCo-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>')"
```

---

### Task 6: 真机验证清单(交互桌面,非本会话可驱)

**Files:**(无代码改动;记录/手动验证)

真机需人在 Windows 桌面旁(SSH 起的托盘落会话 0,交互桌面看不到)。逐项目视确认:

- [ ] **Step 1: exe/开始菜单图标** — 资源管理器看 `C:\Program Files\bx\bx.exe` 显示绿盾+b;右键属性→详细信息 有版本。
- [ ] **Step 2: 托盘四态** — `bx.exe tray` 启动;分别制造四态并看托盘图标:
  - 保护中(健康)→ 绿盾
  - 有更新(需线上真有新 release,或临时降低本地版本号触发 available)→ 琥珀盾
  - 停服务(`sc stop bx`)→ 灰盾;隧道不健康(断服务端)→ 红盾
- [ ] **Step 3: 琥珀态"更新"菜单** — 琥珀态右键菜单出现"更新到最新版";点击弹确认 → UAC → `bx update` 跑通、保护保留。
- [ ] **Step 4: 记录结果** — 把四态截图/结论回填到 `CLAUDE.md` 的 Windows 段(托盘图标里程碑),标注真机已验。

---

## Self-Review

**Spec coverage:**
- 图标美术(盾+b、四色、多尺寸 ICO、PNG、.syso)→ Task 1 ✓
- 统一状态语义(绿/琥珀/红/灰;failover 恒绿;红优先)→ Task 2(`trayStateFrom` 优先级)✓
- 琥珀=有更新 + 6h 节流 + best-effort → Task 3(纯逻辑)+ Task 5(接线)✓
- iconFor 四路 + attention→failed 重命名 → Task 1(文件)+ Task 4(embed/映射)✓
- "更新到最新版"菜单 + per-action UAC → Task 2(menuItemsFor)+ Task 5(systray 项 + elevateRun)✓
- 不改 macOS / manifest 不变量 → Global Constraints,无任务触碰 ✓
- 测试:状态优先级、节流、解析纯逻辑单测 → Task 2/3 ✓;真机待验 → Task 6 ✓

**Placeholder scan:** 无 TBD/TODO;每个代码步给了完整代码。Task 2 的 `detectState(..., false)` 是刻意的临时占位,Task 5 Step 3 明确替换为 `updateAvailable`,非遗留占位。

**Type consistency:**
- `trayStateFrom(bool,bool,bool,bool) TrayState` — Task 2 定义、Task 2 Step 4 由 detectState 调用,参数序一致(svc,cfg,healthy,update)✓
- `detectState(string,string,bool)` — Task 2 定义、Task 2 Step 5 与 Task 5 Step 3 调用一致 ✓
- `parseUpdateCheckJSON([]byte)(bool,bool)`、`shouldCheckUpdate(time.Time,time.Time,time.Duration)bool`、`checkUpdateAvailable(string)bool` — Task 3 定义、Task 5 Step 3 使用一致 ✓
- ICO 文件名 `protected/warning/failed/off` — Task 1 产出、Task 4 embed 一致 ✓
- `StateWarning`、`TrayMenu.Update`、`toggleItems.Update`/`mUpdate` — Task 2/5 前后一致 ✓
