# Windows 托盘"开机自启"开关 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 给 Windows 托盘加一个"开机自启"勾选开关,同控服务开机自启 + 托盘登录自启;把"自启"与"连接/断开"解耦。

**Architecture:** 新增 `install.SetAutostart(bool)`/`AutostartEnabled()` 原子治理"服务 StartType + 托盘 HKCU Run"两处;`bx autostart on|off|status` 命令(提权改 SCM)由托盘 per-action UAC 拉起;`bx up`/`down` 解耦成只 Start/Stop;`setup` 默认设自启 ON;无参 `bx.exe` 启托盘。纯逻辑(参数解析)Linux 单测,SCM/HKCU/systray 部分 windows 交叉编译 + 真机验。

**Tech Stack:** Go 1.26,`golang.org/x/sys/windows/svc/mgr`(SCM StartType)、`golang.org/x/sys/windows/registry`(HKCU Run)、`fyne.io/systray`(checkbox),urfave/cli v2。

## Global Constraints

- **纯 Windows 改动**:不改 macOS(`apps/macos/**`)、不碰隧道/路由/数据面。`bx autostart` 在非 windows 返回"暂不支持",不动 systemd/launchd。
- **一个开关控两处**:自启 ON = 服务 `StartType=StartAutomatic` + 托盘 HKCU Run 写入;OFF = `StartType=StartManual` + HKCU Run 删除。**永远一起动**。
- **关 = `StartManual`,不是 `StartDisabled`**(停了仍可手动/`up` 起)。
- **正交解耦**:`bx up`=仅 Start;`bx down`=仅 Stop;都不改 StartType。默认自启由 `setup` 设(ON)。
- **HKCU Run 值**:`"<install.BinPath>" tray`,即 `"C:\Program Files\bx\bx.exe" tray`。
- **权威信号**:服务 StartType(`AutostartEnabled()` 读它,复用 `ServiceState("is-enabled","bx")=="enabled"`)。
- **manifest 不变量**:`winres.json` execution-level 恒 `as invoker`;autostart 改 SCM 靠 per-action UAC。本计划不改 winres.json。
- **gofumpt 格式化**(非 gofmt);`gofumpt -l <touched dirs>` 须无输出。
- **提交信息**:中文 conventional commits,结尾 `Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>`,直接提交 master。
- **验证**:`go build ./... && go vet ./...`(linux)+ `GOOS=windows GOARCH=amd64 go build ./... && GOOS=windows GOARCH=arm64 go build ./...` + `go test ./...`。

---

## File Structure

- `internal/install/autostart_windows.go` — **创建**(`//go:build windows`)。`SetAutostart(bool)`、`AutostartEnabled() bool`。
- `internal/install/service_windows.go` — **修改**。`windowsEnableService`→仅 Start;`windowsDisableService`→仅 Stop。
- `internal/cli/autostart.go` — **创建**(无 tag)。纯函数 `parseAutostartArg`。
- `internal/cli/autostart_test.go` — **创建**(无 tag)。`parseAutostartArg` 测试。
- `internal/cli/autostart_windows.go` — **创建**(`//go:build windows`)。`autostartAction`(调 install)。
- `internal/cli/autostart_other.go` — **创建**(`//go:build !windows`)。`autostartAction`→"暂不支持"。
- `internal/cli/rootaction_windows.go` — **创建**(`//go:build windows`)。`rootAction`→无参启托盘。
- `internal/cli/rootaction_other.go` — **创建**(`//go:build !windows`)。`rootAction`→显示帮助。
- `internal/cli/setup_autostart_windows.go` — **创建**(`//go:build windows`)。`postSetupAutostart()`→`SetAutostart(true)`。
- `internal/cli/setup_autostart_other.go` — **创建**(`//go:build !windows`)。`postSetupAutostart()`→nil。
- `internal/cli/cli.go` — **修改**。注册 `autostart` 命令;`App.Action = rootAction`;`setupAction` 尾部调 `postSetupAutostart()`。
- `internal/tray/tray_windows.go` — **修改**。加"开机自启"checkbox + 点击提权 + poll 同步勾选;删 `autostartOnce.Do(setAutostart)`。
- `internal/tray/win_windows.go` — **修改**。删除已无用的 `setAutostart`(HKCU 逻辑迁到 install)。

---

### Task 1: install.SetAutostart + AutostartEnabled(windows)

**Files:**
- Create: `internal/install/autostart_windows.go`

**Interfaces:**
- Produces:
  - `func SetAutostart(enabled bool) error` — enabled: 服务 StartType=StartAutomatic + 写 HKCU Run `"<BinPath>" tray`;!enabled: StartType=StartManual + 删 HKCU Run。需提权。
  - `func AutostartEnabled() bool` — 服务 StartType==StartAutomatic(复用 `ServiceState("is-enabled", windowsServiceName)=="enabled"`)。非提权只读。
- Consumes: 现有 `openService()`、`windowsServiceName`、`BinPath`、`ServiceState`。

- [ ] **Step 1: 写 `internal/install/autostart_windows.go`**

```go
//go:build windows

// autostart_windows.go 治理"开机自启"两处:服务 StartType(SCM)+ 托盘登录自启(HKCU Run)。
// 一个 SetAutostart 原子改两处,避免"服务自启开、图标自启关"错位。
package install

import (
	"fmt"

	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc/mgr"
)

// hkcuRunKey 是当前用户登录自启注册表路径;runValueName 是 bx 的值名。
const (
	hkcuRunKey   = `Software\Microsoft\Windows\CurrentVersion\Run`
	runValueName = "bx"
)

// SetAutostart 原子设置开机自启:enabled → 服务 StartAutomatic + 写 HKCU Run;
// !enabled → 服务 StartManual(非 Disabled,仍可手动/up 起)+ 删 HKCU Run。需管理员(改 SCM 配置)。
func SetAutostart(enabled bool) error {
	m, s, err := openService()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	defer s.Close()
	cfg, err := s.Config()
	if err != nil {
		return fmt.Errorf("读服务配置: %w", err)
	}
	if enabled {
		cfg.StartType = mgr.StartAutomatic
	} else {
		cfg.StartType = mgr.StartManual
	}
	if err := s.UpdateConfig(cfg); err != nil {
		return fmt.Errorf("设服务 StartType: %w", err)
	}
	return setTrayLoginAutostart(enabled)
}

// setTrayLoginAutostart 写/删当前用户的托盘登录自启 HKCU Run 项(值 = `"<BinPath>" tray`)。
func setTrayLoginAutostart(enabled bool) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, hkcuRunKey, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("打开 HKCU Run: %w", err)
	}
	defer k.Close()
	if enabled {
		if err := k.SetStringValue(runValueName, `"`+BinPath+`" tray`); err != nil {
			return fmt.Errorf("写 HKCU Run: %w", err)
		}
		return nil
	}
	if err := k.DeleteValue(runValueName); err != nil && err != registry.ErrNotExist {
		return fmt.Errorf("删 HKCU Run: %w", err)
	}
	return nil
}

// AutostartEnabled 报告服务是否开机自启(StartType==StartAutomatic)。非提权只读,给托盘勾选框用。
func AutostartEnabled() bool {
	return ServiceState("is-enabled", windowsServiceName) == "enabled"
}
```

- [ ] **Step 2: windows 交叉编译(amd64 + arm64)**

Run: `GOOS=windows GOARCH=amd64 go build ./internal/install/ && GOOS=windows GOARCH=arm64 go build ./internal/install/`
Expected: 均成功。

- [ ] **Step 3: gofumpt + vet**

Run: `go run mvdan.cc/gofumpt@latest -l internal/install/ && GOOS=windows GOARCH=amd64 go vet ./internal/install/`
Expected: gofumpt 无输出;vet 无新问题。

- [ ] **Step 4: Commit**

```bash
git add internal/install/autostart_windows.go
git commit -m "$(printf 'feat(install): SetAutostart 原子治理服务 StartType + 托盘 HKCU 自启\n\nSetAutostart(true)=StartAutomatic+写 HKCU Run;false=StartManual(非 Disabled)\n+删 HKCU Run,两处一起动。AutostartEnabled 复用 ServiceState is-enabled 非提权读。\n\nCo-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>')"
```

---

### Task 2: 解耦 up/down(去掉 StartType 改动)

**Files:**
- Modify: `internal/install/service_windows.go`(`windowsEnableService`、`windowsDisableService`)

**Interfaces:**
- Consumes: 现有 `openService`、`stopAndWait`。
- Produces:(无新符号;改行为)`windowsEnableService`=仅 Start;`windowsDisableService`=仅 Stop。

- [ ] **Step 1: 改 `windowsEnableService`(仅 Start,不设 StartAutomatic)**

把现有:

```go
	cfg.StartType = mgr.StartAutomatic
	if err := s.UpdateConfig(cfg); err != nil {
		return fmt.Errorf("设开机自启: %w", err)
	}
	if err := s.Start(); err != nil && !errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING) {
		return fmt.Errorf("启动服务: %w", err)
	}
	return nil
```

改为(仅 Start;自启由 setup/`bx autostart` 单独管):

```go
	if err := s.Start(); err != nil && !errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING) {
		return fmt.Errorf("启动服务: %w", err)
	}
	return nil
```

同时删掉该函数里已不用的 `cfg, err := s.Config()` 读取(若删后 `cfg` 未被使用会编译报错,一并清理)。

- [ ] **Step 2: 改 `windowsDisableService`(仅 Stop,不设 StartDisabled)**

把现有:

```go
	_ = stopAndWait(s)
	if cfg, err := s.Config(); err == nil {
		cfg.StartType = mgr.StartDisabled
		_ = s.UpdateConfig(cfg)
	}
	return nil
```

改为:

```go
	_ = stopAndWait(s)
	return nil
```

- [ ] **Step 3: 若 `mgr`/`windows` import 变成未使用则清理**

Run: `GOOS=windows GOARCH=amd64 go build ./internal/install/`
Expected: 成功。若报 `imported and not used: "...mgr"` 或 `windows`,检查文件里其它函数是否仍用;`windowsInstallService` 用 `mgr.Config`/`mgr.StartManual`、`stopAndWait` 用 `svc`,`Task 1` 的 autostart 在别的文件——本文件大概率仍用 `mgr`(CreateService)与 `windows`(ERROR_SERVICE_ALREADY_RUNNING 仍在 Enable 的 Start 里)。保留仍用到的 import。

- [ ] **Step 4: windows 交叉编译 + gofumpt**

Run: `GOOS=windows GOARCH=amd64 go build ./internal/install/ && GOOS=windows GOARCH=arm64 go build ./internal/install/ && go run mvdan.cc/gofumpt@latest -l internal/install/`
Expected: build 成功;gofumpt 无输出。

- [ ] **Step 5: Commit**

```bash
git add internal/install/service_windows.go
git commit -m "$(printf 'refactor(install): 解耦 windows up/down 与开机自启\n\nwindowsEnableService 只 Start、windowsDisableService 只 Stop,均不再改 StartType。\n开机自启改由 setup 默认 + bx autostart 单独治理(正交)。修正 down 用 Disabled\n致停后起不来的缺陷。\n\nCo-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>')"
```

---

### Task 3: bx autostart 命令 + parseAutostartArg

**Files:**
- Create: `internal/cli/autostart.go`(纯函数)
- Create: `internal/cli/autostart_test.go`
- Create: `internal/cli/autostart_windows.go`
- Create: `internal/cli/autostart_other.go`
- Modify: `internal/cli/cli.go`(注册 `autostart` 命令)

**Interfaces:**
- Produces:
  - `func parseAutostartArg(arg string) (want *bool, status bool, err error)` — "on"→(&true,false,nil);"off"→(&false,false,nil);"status"/""→(nil,true,nil);其它→(nil,false,err)。
  - `func autostartAction(c *cli.Context) error` — windows:据 parse 调 `install.SetAutostart`/打印 `AutostartEnabled`;非 windows:返回"暂不支持"。
- Consumes: Task 1 的 `install.SetAutostart`、`install.AutostartEnabled`。

- [ ] **Step 1: 写失败测试 `internal/cli/autostart_test.go`**

```go
package cli

import "testing"

func TestParseAutostartArg(t *testing.T) {
	boolPtr := func(b bool) *bool { return &b }
	cases := []struct {
		arg     string
		want    *bool
		status  bool
		wantErr bool
	}{
		{"on", boolPtr(true), false, false},
		{"off", boolPtr(false), false, false},
		{"status", nil, true, false},
		{"", nil, true, false},
		{"maybe", nil, false, true},
	}
	for _, c := range cases {
		t.Run(c.arg, func(t *testing.T) {
			want, status, err := parseAutostartArg(c.arg)
			if (err != nil) != c.wantErr {
				t.Fatalf("arg %q: err=%v wantErr=%v", c.arg, err, c.wantErr)
			}
			if c.wantErr {
				return
			}
			if status != c.status {
				t.Fatalf("arg %q: status=%v want %v", c.arg, status, c.status)
			}
			if (want == nil) != (c.want == nil) || (want != nil && *want != *c.want) {
				t.Fatalf("arg %q: want=%v expected %v", c.arg, want, c.want)
			}
		})
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/cli/ -run TestParseAutostartArg -v`
Expected: 编译失败 `undefined: parseAutostartArg`。

- [ ] **Step 3: 写 `internal/cli/autostart.go`**

```go
package cli

import "fmt"

// parseAutostartArg 把 `bx autostart <arg>` 的参数解析成意图。
// "on"→want=true;"off"→want=false;"status"/空→status=true;其它→err。
func parseAutostartArg(arg string) (want *bool, status bool, err error) {
	switch arg {
	case "on":
		v := true
		return &v, false, nil
	case "off":
		v := false
		return &v, false, nil
	case "", "status":
		return nil, true, nil
	default:
		return nil, false, fmt.Errorf("未知参数 %q(用 on|off|status)", arg)
	}
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/cli/ -run TestParseAutostartArg -v`
Expected: PASS(5 子用例)。

- [ ] **Step 5: 写 `internal/cli/autostart_windows.go`**

```go
//go:build windows

package cli

import (
	"fmt"

	"github.com/getbx/bx/internal/install"
	"github.com/urfave/cli/v2"
)

func autostartAction(c *cli.Context) error {
	want, status, err := parseAutostartArg(c.Args().First())
	if err != nil {
		return err
	}
	if status {
		if install.AutostartEnabled() {
			fmt.Println("开机自启:开")
		} else {
			fmt.Println("开机自启:关")
		}
		return nil
	}
	if err := install.SetAutostart(*want); err != nil {
		return err
	}
	if *want {
		fmt.Println("✅ 已设为开机自启(服务 + 托盘图标)。")
	} else {
		fmt.Println("✅ 已取消开机自启。")
	}
	return nil
}
```

- [ ] **Step 6: 写 `internal/cli/autostart_other.go`**

```go
//go:build !windows

package cli

import (
	"errors"

	"github.com/urfave/cli/v2"
)

func autostartAction(_ *cli.Context) error {
	return errors.New("bx autostart 目前仅支持 Windows")
}
```

- [ ] **Step 7: 在 `internal/cli/cli.go` 的 `Commands` 注册命令**

在 `{Name: "tray", ...}` 之后加:

```go
			{Name: "autostart", Usage: "开机自启开关(Windows;on|off|status)", ArgsUsage: "on|off|status", Action: autostartAction},
```

- [ ] **Step 8: 全平台验证**

Run: `go test ./internal/cli/ -run TestParseAutostartArg && go build ./... && GOOS=windows GOARCH=amd64 go build ./... && GOOS=windows GOARCH=arm64 go build ./... && go run mvdan.cc/gofumpt@latest -l internal/cli/`
Expected: 测试 PASS;三处 build 成功;gofumpt 无输出。

- [ ] **Step 9: Commit**

```bash
git add internal/cli/autostart.go internal/cli/autostart_test.go internal/cli/autostart_windows.go internal/cli/autostart_other.go internal/cli/cli.go
git commit -m "$(printf 'feat(cli): bx autostart on|off|status 命令\n\nparseAutostartArg 纯函数(TDD)分派;windows 调 install.SetAutostart/AutostartEnabled,\n非 windows 返回"仅支持 Windows"。托盘经 per-action UAC 拉起。\n\nCo-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>')"
```

---

### Task 4: setup 默认设自启 ON(windows)

**Files:**
- Create: `internal/cli/setup_autostart_windows.go`
- Create: `internal/cli/setup_autostart_other.go`
- Modify: `internal/cli/cli.go`(`setupAction` 尾部调用)

**Interfaces:**
- Produces: `func postSetupAutostart() error` — windows:`install.SetAutostart(true)`;非 windows:nil。
- Consumes: Task 1 `install.SetAutostart`。

- [ ] **Step 1: 写 `internal/cli/setup_autostart_windows.go`**

```go
//go:build windows

package cli

import "github.com/getbx/bx/internal/install"

// postSetupAutostart 在 windows setup 装完服务后设默认开机自启 ON
// (保住"装好即开机自启"的好默认;up/down 已与自启解耦,故须在 setup 显式设)。
func postSetupAutostart() error { return install.SetAutostart(true) }
```

- [ ] **Step 2: 写 `internal/cli/setup_autostart_other.go`**

```go
//go:build !windows

package cli

// postSetupAutostart 在非 windows 无操作(linux/mac 的开机自启由各自 up/enable 语义处理)。
func postSetupAutostart() error { return nil }
```

- [ ] **Step 3: 在 `setupAction` 尾部(`WriteUnit` 之后、成功打印之前)调用**

在 `internal/cli/cli.go` 的 `setupAction` 里,`install.WriteUnit(...)` 成功之后加:

```go
	if err := postSetupAutostart(); err != nil {
		return fmt.Errorf("设默认开机自启: %w", err)
	}
```

（`fmt` 已在 cli.go import。)

- [ ] **Step 4: 全平台验证**

Run: `go build ./... && GOOS=windows GOARCH=amd64 go build ./... && GOOS=windows GOARCH=arm64 go build ./... && go run mvdan.cc/gofumpt@latest -l internal/cli/`
Expected: 均成功;gofumpt 无输出。

- [ ] **Step 5: Commit**

```bash
git add internal/cli/setup_autostart_windows.go internal/cli/setup_autostart_other.go internal/cli/cli.go
git commit -m "$(printf 'feat(cli): windows setup 默认设开机自启 ON\n\nup/down 与自启解耦后,setup 尾部显式 SetAutostart(true) 保住"装好即自启"\n默认;非 windows 无操作。\n\nCo-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>')"
```

---

### Task 5: 无参 bx.exe(windows)启动托盘

**Files:**
- Create: `internal/cli/rootaction_windows.go`
- Create: `internal/cli/rootaction_other.go`
- Modify: `internal/cli/cli.go`(`App.Action = rootAction`)

**Interfaces:**
- Produces: `func rootAction(c *cli.Context) error` — windows:无参→`trayAction(c)`,有参(未知命令)→打印帮助 + 报错;非 windows:打印帮助(有未知参报错)。
- Consumes: 现有 `trayAction`。

- [ ] **Step 1: 写 `internal/cli/rootaction_windows.go`**

```go
//go:build windows

package cli

import (
	"fmt"

	"github.com/urfave/cli/v2"
)

// rootAction 是无子命令时的行为:windows 下双击/无参启动托盘(回到图标);
// 有未知参数则出帮助并报错。
func rootAction(c *cli.Context) error {
	if c.Args().Len() == 0 {
		return trayAction(c)
	}
	_ = cli.ShowAppHelp(c)
	return fmt.Errorf("未知命令: %s", c.Args().First())
}
```

- [ ] **Step 2: 写 `internal/cli/rootaction_other.go`**

```go
//go:build !windows

package cli

import (
	"fmt"

	"github.com/urfave/cli/v2"
)

// rootAction 在非 windows 保持默认:无参出帮助,有未知参数出帮助并报错。
func rootAction(c *cli.Context) error {
	_ = cli.ShowAppHelp(c)
	if c.Args().Len() > 0 {
		return fmt.Errorf("未知命令: %s", c.Args().First())
	}
	return nil
}
```

- [ ] **Step 3: 在 `New()` 的 `cli.App` 加 `Action`**

在 `internal/cli/cli.go` 的 `&cli.App{...}` 里(`Version:` 之后、`Commands:` 之前或之后均可)加:

```go
		Action: rootAction,
```

- [ ] **Step 4: 验证无参行为**

Run(linux 上验证不会误启 tray、仍出帮助):
```bash
go build -o /tmp/bx-roottest . && /tmp/bx-roottest 2>&1 | head -3
```
Expected: 打印 bx 帮助(NAME/USAGE…),非报错、非托盘。

- [ ] **Step 5: 全平台编译 + gofumpt**

Run: `go build ./... && GOOS=windows GOARCH=amd64 go build ./... && GOOS=windows GOARCH=arm64 go build ./... && go run mvdan.cc/gofumpt@latest -l internal/cli/`
Expected: 均成功;gofumpt 无输出。

- [ ] **Step 6: Commit**

```bash
git add internal/cli/rootaction_windows.go internal/cli/rootaction_other.go internal/cli/cli.go
git commit -m "$(printf 'feat(cli): 无参 bx.exe(windows)启动托盘\n\nApp.Action=rootAction:windows 无参→启托盘(支撑"打开 bx 即回图标"恢复模型),\n非 windows 保持出帮助。bx help/子命令不受影响。\n\nCo-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>')"
```

---

### Task 6: 托盘"开机自启"勾选项接线

**Files:**
- Modify: `internal/tray/tray_windows.go`
- Modify: `internal/tray/win_windows.go`(删已无用的 `setAutostart`)

**Interfaces:**
- Consumes: Task 1 `install.AutostartEnabled`、`install.SetAutostart`(经 `bx autostart` 命令,不直接调);现有 `elevateRun`、`confirm`、`showOrHide`、`install` import。
- Produces:(终态,无下游)

- [ ] **Step 1: `onReady` 加"开机自启"checkbox + 点击处理**

在 `mRestart := ...` 之后加 checkbox(初值读当前态):

```go
	mAutostart := systray.AddMenuItemCheckbox("开机自启", "开机自动保护 + 显示图标", install.AutostartEnabled())
```

在 restart 的点击 goroutine 之后加 autostart 点击处理(据真实态取反,提权跑 `bx autostart on/off`):

```go
	go func() {
		for range mAutostart.ClickedCh {
			verb := "on"
			if install.AutostartEnabled() {
				verb = "off"
			}
			if confirm("开机自启", "切换 bx 开机自启(服务 + 托盘图标)?") {
				_ = elevateRun("autostart " + verb)
			}
		}
	}()
```

（`internal/install` 已被 tray 包 import;若无则加 `"github.com/getbx/bx/internal/install"`。）

- [ ] **Step 2: `toggleItems` 加 `Autostart` 字段,`onReady` 传入**

```go
type toggleItems struct {
	Connect    *systray.MenuItem
	Disconnect *systray.MenuItem
	Setup      *systray.MenuItem
	Restart    *systray.MenuItem
	Update     *systray.MenuItem
	Autostart  *systray.MenuItem
}
```

`go pollLoop(...)` 的 `toggleItems{...}` 加 `Autostart: mAutostart,`。

- [ ] **Step 3: `pollLoop` 同步勾选态 + 删无脑自注册**

在 pollLoop 循环体内,同步勾选态(不是 show/hide,是 Check/Uncheck):

```go
		if install.AutostartEnabled() {
			items.Autostart.Check()
		} else {
			items.Autostart.Uncheck()
		}
```

并**删除**原有的:

```go
		autostartOnce.Do(func() {
			_ = setAutostart(exe)
		})
```

以及函数顶部不再需要的 `var autostartOnce sync.Once`;若 `sync` 在本文件已无其它用处则移除其 import。

- [ ] **Step 4: 删 `internal/tray/win_windows.go` 的 `setAutostart`**

删除整个 `func setAutostart(exePath string) error { ... }`(HKCU 逻辑已迁到 `install.SetAutostart`/`setTrayLoginAutostart`)。若删后 `registry` import 在本文件无其它用处则移除。

- [ ] **Step 5: windows 交叉编译(amd64 + arm64)+ vet + gofumpt**

Run: `GOOS=windows GOARCH=amd64 go build ./... && GOOS=windows GOARCH=arm64 go build ./... && GOOS=windows GOARCH=amd64 go vet ./internal/tray/ && go run mvdan.cc/gofumpt@latest -l internal/tray/`
Expected: 均成功;gofumpt 无输出。

- [ ] **Step 6: 全量验证**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: 全绿。

- [ ] **Step 7: Commit**

```bash
git add internal/tray/tray_windows.go internal/tray/win_windows.go
git commit -m "$(printf 'feat(tray): "开机自启"勾选开关(同控服务 + 托盘自启)\n\nAddMenuItemCheckbox 勾选态读 install.AutostartEnabled,点击 per-action UAC 跑\nbx autostart on/off;pollLoop 每轮同步勾选。删除无脑 setAutostart 自注册\n(自启改由开关治理)。\n\nCo-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>')"
```

---

### Task 7: 真机验证清单(交互桌面,非本会话可驱)

**Files:**(无代码;记录/手动验证)

- [ ] **Step 1: 勾选"开机自启"** — 托盘勾上 → UAC → `sc qc bx` 显示 `AUTO_START`;`reg query "HKCU\...\Run" /v bx` 有 `"...\bx.exe" tray`。
- [ ] **Step 2: 重启验证 ON** — 重启机器 → 服务自动起(整机出口==VPS)+ 登录后托盘图标自动出现。
- [ ] **Step 3: 取消勾选** — 托盘取消勾 → UAC → `sc qc bx` 显示 `DEMAND_START`(Manual,非 DISABLED);HKCU Run 无 `bx`。
- [ ] **Step 4: 重启验证 OFF + 恢复** — 重启 → 服务不自起、无托盘图标;双击 `bx.exe`(或开始菜单)→ 托盘出现(灰盾);勾"开机自启"可再开。
- [ ] **Step 5: 正交性** — 服务运行中点"断开"(down)→ 停止但 `sc qc bx` 仍 `AUTO_START`(未被 down 改);重启仍自起。点"连接"(up)不改 StartType。
- [ ] **Step 6: 非提权读态** — 托盘(非提权)勾选框正确反映 `sc qc bx` 的 StartType(确认 `ServiceState("is-enabled")` 非提权可读 QUERY_CONFIG)。
- [ ] **Step 7: 回填 CLAUDE.md** — 真机结论记入 Windows 段。

---

## Self-Review

**Spec coverage:**
- 一个开关同控服务+托盘自启 → Task 1(`SetAutostart` 改两处)+ Task 6(checkbox)✓
- 自启与 up/down 正交解耦 → Task 2 ✓
- 关=Manual 非 Disabled → Task 1(`SetAutostart(false)`→StartManual)+ Task 2(down 不再 Disabled)✓
- setup 默认自启 ON → Task 4 ✓
- `bx autostart on|off|status` → Task 3 ✓
- 无参 exe 启托盘 → Task 5 ✓
- 权威信号=服务 StartType(AutostartEnabled 复用 is-enabled) → Task 1 ✓
- 不改 macOS / manifest 不变量 → Global Constraints,无任务触碰 ✓
- 测试:parseAutostartArg 纯逻辑 → Task 3;真机验 → Task 7 ✓

**Placeholder scan:** 无 TBD/TODO;每个代码步给完整代码。Task 2/3/6 的 import 清理带条件判断(build 报错才清),非占位。

**Type consistency:**
- `SetAutostart(bool) error`、`AutostartEnabled() bool` — Task 1 定义、Task 3/4/6 使用一致 ✓
- `parseAutostartArg(string)(*bool,bool,error)` — Task 3 定义、Task 3 autostartAction 使用一致 ✓
- `autostartAction`/`rootAction`/`postSetupAutostart` build-tagged 双实现签名一致(windows/other)✓
- `toggleItems.Autostart *systray.MenuItem` — Task 6 定义、pollLoop 使用一致 ✓
- HKCU 值格式 `"<BinPath>" tray` — Task 1 与被删的旧 `setAutostart`(Task 6 删)一致 ✓
