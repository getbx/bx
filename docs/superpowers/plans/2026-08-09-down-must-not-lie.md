# `bx down` 不许在保护还开着时报成功 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `Manager.Down` 在报告 `ProtectionOff` 之前,向系统求证还有没有 Core 在跑;求证不了就如实说「没能确认」,而不是撒谎,也不是让停止失败。

**Architecture:** 只改 `internal/guardian`。复用已有的 `scanRunningCores`(按 argv 判定,与谁拉起 Core 无关),经一个**可选接口**从 `Manager` 取用。不新增第二个注入点,不动数据面,不动 CLI,不动菜单。

**Tech Stack:** Go 1.26,既有的 `procscan_darwin.go` / `procscan_other.go` 平台分裂。

## Global Constraints

- **扫描的任何失败模式都不许让 `Down` 返回 error,也不许让它提前返回而跳过拆除步骤。**
  这是 2026-08-04(用户 71 分钟关不掉保护)那条不变量。测试要专门钉住。
- **健康机器上 `bx down` 必须仍然返回 `ProtectionOff`。** 否则这条修复自己变成噪声源,
  而被训练成忽略 `needs_attention` 的用户,下次真出事时也会忽略。
- **扫描发生在拆除做完之后**,不是之前。停之前扫没有意义。
- `needsAttention` 通路不重造:它已经在 `barrierProven()` 时把 `Protection` 设成
  `Blocked` 而非 `NeedsAttention`,那个语义要保住。
- TDD;中文 conventional commits,结尾 `Co-Authored-By: Claude <noreply@anthropic.com>`;直接在 `master` 提交。
- 验证:`go build ./... && go vet ./... && go test ./... -count=1`;`go test -race ./internal/guardian -count=1`;交叉编译 linux/darwin/windows × amd64/arm64;`gofumpt -l`(排除 `internal/embedded/assets/` 与 `internal/winfw/`)。**自己跑 gofumpt 并读输出** —— 本项目已有三次「报告说格式干净」而 CI 会红。
- **agent 不得在宿主上跑 `bx`/`sudo bx`/`launchctl`/`route`/`networksetup`**。

---

## Task 1: 求证原语接到 `Manager` 上

**Files:**
- Modify: `internal/guardian/manager.go`(新增 `coreScanner` 接口 + `confirmCoreStopped`)
- Modify: `internal/guardian/process.go`(`ExecCoreRunner.ScanRunning` 薄包装)
- Modify: `internal/guardian/manager_test.go`、`internal/guardian/update_test.go`(两个替身各加一个方法)
- Create: `internal/guardian/manager_confirm_test.go`

**Interfaces:**
- Produces:
  - `type coreScanner interface { ScanRunning() ([]Process, error) }`
  - `func (r *ExecCoreRunner) ScanRunning() ([]Process, error)` —— 就是 `r.scanCores()`
  - `func (m *Manager) confirmCoreStopped() (stopped bool, reason string)` —— **永不返回 error**
  - `var coreScanSettle = 300 * time.Millisecond` —— 包级变量,测试可覆盖

- [ ] **Step 1: 写失败测试**

`internal/guardian/manager_confirm_test.go`:

```go
package guardian

import (
	"errors"
	"testing"
	"time"
)

type scriptedScanner struct {
	results [][]Process
	errs    []error
	calls   int
}

func (s *scriptedScanner) ScanRunning() ([]Process, error) {
	i := s.calls
	s.calls++
	if i < len(s.errs) && s.errs[i] != nil {
		return nil, s.errs[i]
	}
	if i < len(s.results) {
		return s.results[i], nil
	}
	return nil, nil
}

// 扫不到 Core = 求证过了,可以说 off。
func TestConfirmCoreStoppedAcceptsAnEmptyScan(t *testing.T) {
	m := &Manager{runner: &scannerOnlyRunner{scan: &scriptedScanner{}}}
	stopped, reason := m.confirmCoreStopped()
	if !stopped {
		t.Fatalf("扫不到 Core 时必须允许报 off, reason=%s", reason)
	}
}

// 扫到 Core 还在 = 不许说 off。**这条是整个改动的理由。**
func TestConfirmCoreStoppedRefusesWhenACoreIsStillRunning(t *testing.T) {
	scan := &scriptedScanner{results: [][]Process{{{PID: 4242}}, {{PID: 4242}}}}
	m := &Manager{runner: &scannerOnlyRunner{scan: scan}}
	stopped, reason := m.confirmCoreStopped()
	if stopped {
		t.Fatal("系统里还有 Core 在跑时绝不能报 off —— 那是对用户撒谎,而他此刻以为保护关了")
	}
	if reason != "core_still_running" {
		t.Errorf("理由要能区分「确知还在」与「问不出来」, got %q", reason)
	}
}

// 刚被 SIGTERM、还在跑 defer 的进程既不是僵尸也还没消失。
// 扫到就断言「还在」会把正常的关闭误报成故障,所以要等一小会儿重扫一次。
func TestConfirmCoreStoppedReScansAfterASettleWindow(t *testing.T) {
	restore := coreScanSettle
	coreScanSettle = time.Millisecond
	defer func() { coreScanSettle = restore }()

	scan := &scriptedScanner{results: [][]Process{{{PID: 4242}}, nil}}
	m := &Manager{runner: &scannerOnlyRunner{scan: scan}}
	stopped, _ := m.confirmCoreStopped()
	if !stopped {
		t.Fatal("第二次扫描已经干净了,不该报「还在跑」—— 那会把每次正常关闭都变成告警")
	}
	if scan.calls != 2 {
		t.Errorf("应当重扫一次, calls=%d", scan.calls)
	}
}

// **扫描失败不许让停止失败,也不许被读成「没有」。**
func TestConfirmCoreStoppedTreatsAFailedScanAsUnconfirmed(t *testing.T) {
	scan := &scriptedScanner{errs: []error{errors.New("sysctl 挂了"), errors.New("还是挂")}}
	m := &Manager{runner: &scannerOnlyRunner{scan: scan}}
	stopped, reason := m.confirmCoreStopped()
	if stopped {
		t.Fatal("问不出来不等于没有 —— 不许塌缩成「已关闭」")
	}
	if reason != "core_scan_failed" {
		t.Errorf("理由必须与「确知还在」区分开, got %q", reason)
	}
}

// runner 压根不会扫(测试替身、将来别的平台):同样是「没能确认」,而不是崩溃或失败。
func TestConfirmCoreStoppedHandlesARunnerThatCannotScan(t *testing.T) {
	m := &Manager{runner: &nonScanningRunner{}}
	stopped, reason := m.confirmCoreStopped()
	if stopped {
		t.Fatal("不能求证时不许声称已关闭")
	}
	if reason != "core_scan_unsupported" {
		t.Errorf("got %q", reason)
	}
}
```

`scannerOnlyRunner` / `nonScanningRunner`:两个最小的 `CoreRunner` 实现,方法体
除 `ScanRunning` 外全部返回零值(本任务不经过它们)。

- [ ] **Step 2: 跑它,确认失败**

Run: `go test ./internal/guardian -run TestConfirmCoreStopped -count=1`
Expected: FAIL,`undefined: coreScanner` / `m.confirmCoreStopped undefined`

- [ ] **Step 3: 实现**

`process.go`:

```go
// ScanRunning 把 scanCores 暴露给 Manager:它在报告「已关闭」之前要向系统求证,
// 而不能只信 /var/lib/bx/core-process.json —— 那份记录里没有 legacy Core、
// 没有 `sudo bx run` 起的 Core,手删过它的机器上更是什么都没有。
func (r *ExecCoreRunner) ScanRunning() ([]Process, error) { return r.scanCores() }
```

`manager.go`:

```go
// coreScanner 由能向系统求证「有没有 Core 在跑」的 runner 实现。
//
// 刻意做成**可选**接口而不是塞进 CoreRunner:实现不了的 runner 落到
// 「没能确认」那一支即可,不该被迫编造一个答案。
type coreScanner interface {
	ScanRunning() ([]Process, error)
}

// coreScanSettle 是两次扫描之间的等待。刚收到 SIGTERM、正在跑 defer 的进程
// 既不是僵尸(scanRunningCores 已跳过僵尸)也还没消失,扫到它就断言「还在跑」
// 会把每一次正常关闭都变成告警 —— 而被训练成忽略告警的用户,真出事时也会忽略。
var coreScanSettle = 300 * time.Millisecond

// confirmCoreStopped 在拆除做完之后问系统:还有没有 Core 在跑?
//
// **永不返回 error。** 扫描的任何失败模式都不得让 Down 失败 —— 那是 2026-08-04
// 那条不变量(用户 71 分钟关不掉保护)。它只回答「能不能说 off」,以及为什么不能。
func (m *Manager) confirmCoreStopped() (bool, string) {
	scanner, ok := m.runner.(coreScanner)
	if !ok {
		return false, "core_scan_unsupported"
	}
	cores, err := scanner.ScanRunning()
	if err == nil && len(cores) == 0 {
		return true, ""
	}
	// 一次不算 —— 见 coreScanSettle。
	time.Sleep(coreScanSettle)
	cores, err = scanner.ScanRunning()
	switch {
	case err != nil:
		log.Printf("guardian_core_scan_failed_on_down err=%v", err)
		return false, "core_scan_failed"
	case len(cores) > 0:
		log.Printf("guardian_core_still_running_after_down pids=%s", scannedCorePIDs(cores))
		return false, "core_still_running"
	}
	return true, ""
}
```

两个测试替身(`fakeCoreRunner`、`updateCoreRunner`)各加:

```go
func (r *fakeCoreRunner) ScanRunning() ([]Process, error) { return nil, nil }
```

**注释里要写明为什么替身返回「没有」**:它们模拟的是一台干净机器,而本改动的默认
方向是「不能求证就不说 off」—— 若替身不实现这个方法,每一条既有的 Down 测试都会
变成 needs_attention,那不是发现问题,只是把默认值撞在测试上。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./... -count=1 && go test -race ./internal/guardian -count=1`
Expected: PASS,**既有测试零改动**(除两个替身各加一个方法)。

- [ ] **Step 5: 变异验证**

① 把 `!ok` 那一支改成 `return true, ""` → `…HandlesARunnerThatCannotScan` 必须红。
② 把扫描失败那一支改成 `return true, ""` → `…TreatsAFailedScanAsUnconfirmed` 必须红。
③ 去掉重扫(第一次扫到就返回 false)→ `…ReScansAfterASettleWindow` 必须红。
三处改回。

- [ ] **Step 6: 提交**

```bash
git add internal/guardian/
git commit -m "feat(guardian): 给 Manager 接上「向系统求证有没有 Core 在跑」的原语"
```

---

## Task 2: `Down` 的两条出口都要过这一关

**Files:**
- Modify: `internal/guardian/manager.go`(`Down` 的两处 `ProtectionOff`)
- Create/Modify: `internal/guardian/manager_down_test.go`

**Interfaces:**
- Consumes: Task 1 的 `confirmCoreStopped`

**两处:**
1. `manager.go:483` 附近 —— `desired == DesiredOff && m.current.PID == 0` 的提前返回。
   它今天的两个前提**都是记账**,而这条不变量正是关于记账不可信。
2. `manager.go:571` 附近 —— `removeBarrier` 成功之后那句
   `m.setStatus(Status{… Protection: ProtectionOff})`。

- [ ] **Step 1: 写失败测试**

```go
// 系统里还有 Core 在跑时,Down 必须**如实说没能关掉**,而不是报 off。
// 这是本改动的全部理由:Existing() 读的是 Guardian 自己的记账,而 legacy Core、
// sudo bx run 起的 Core、以及手删过 core-process.json 的机器上,记账里都没有它。
func TestDownDoesNotClaimOffWhileACoreIsStillRunning(t *testing.T) {
	env := newManagerTestEnv(t)          // 既有 helper
	env.runner.scanResult = []Process{{PID: 4242}}

	if err := env.manager.Down(context.Background()); err != nil {
		t.Fatalf("扫到 Core 不该让停止失败: %v", err)
	}
	status := env.manager.Status()
	if status.Protection == ProtectionOff {
		t.Fatal("保护还开着时不许报 off")
	}
	if status.LastError != "core_still_running" {
		t.Errorf("理由要说清, got %q", status.LastError)
	}
	// **拆除必须照常做完** —— 「没能确认」不是「什么都没做」。
	env.assertBarrierRemoved(t)
	env.assertDNSRestored(t)
	env.assertDesiredOffPersisted(t)
}

// 扫描失败不许让停止失败(2026-08-04)。
func TestDownStillSucceedsWhenTheScanFails(t *testing.T) {
	env := newManagerTestEnv(t)
	env.runner.scanErr = errors.New("sysctl 挂了")

	if err := env.manager.Down(context.Background()); err != nil {
		t.Fatalf("扫描失败绝不能让 Down 失败 —— 那正是 71 分钟事故那条不变量: %v", err)
	}
	if env.manager.Status().Protection == ProtectionOff {
		t.Error("问不出来时不许声称已关闭")
	}
	env.assertBarrierRemoved(t)
}

// 提前返回那条路(desired 已 off 且记账里没 Core)同样是记账,同样要求证。
func TestDownEarlyExitAlsoAsksTheSystem(t *testing.T) {
	env := newManagerTestEnv(t)
	env.store.desired = DesiredOff
	env.runner.scanResult = []Process{{PID: 4242}}

	if err := env.manager.Down(context.Background()); err != nil {
		t.Fatal(err)
	}
	if env.manager.Status().Protection == ProtectionOff {
		t.Fatal("这条路也不许只凭记账就说 off")
	}
}

// **回归**:干净机器仍然报 off。否则这条修复自己成了噪声源,
// 而被训练成忽略 needs_attention 的用户,下次真出事也会忽略。
func TestDownStillReportsOffOnAHealthyMachine(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.manager.Down(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := env.manager.Status().Protection; got != ProtectionOff {
		t.Fatalf("干净机器必须仍然报 off, got %q", got)
	}
}
```

`fakeCoreRunner` 加 `scanResult []Process` / `scanErr error` 两个字段并在 `ScanRunning`
里返回它们(默认零值 = 干净机器)。`env.assert*` 若不存在就照既有 helper 的写法补,
**断言行为不断言实现**。

- [ ] **Step 2: 跑它,确认失败**

Run: `go test ./internal/guardian -run 'TestDownDoesNotClaim|TestDownStill|TestDownEarlyExit' -count=1`
Expected: 前三条 FAIL(仍报 off / 理由缺失),最后一条 PASS。

- [ ] **Step 3: 实现**

两处都改成:

```go
	stopped, reason := m.confirmCoreStopped()
	if !stopped {
		m.needsAttention(DesiredOff, reason)
		return nil // 拆除已经做完;没能确认不是失败
	}
	m.setStatus(Status{SchemaVersion: 1, Desired: DesiredOff, Phase: PhaseIdle, Protection: ProtectionOff})
	return nil
```

**注意 `needsAttention` 在 `barrierProven()` 时会把 `Protection` 设成 `Blocked` 而非
`NeedsAttention`** —— 那个语义是既有的、正确的(屏障还在就是 blocked),不要绕过它。

- [ ] **Step 4: 跑全量**

Run: `go test ./... -count=1 && go test -race ./internal/guardian -count=1`

- [ ] **Step 5: 变异验证**

① 把「扫到还在跑仍报 off」放回去 → `…DoesNotClaimOffWhileACoreIsStillRunning` 必须红。
② 把扫描失败那一支改成 `return err` → `…StillSucceedsWhenTheScanFails` 必须红。
③ 只改主出口、不改提前返回 → `…EarlyExitAlsoAsksTheSystem` 必须红。
三处改回。

- [ ] **Step 6: 提交**

```bash
git add internal/guardian/
git commit -m "fix(guardian): Down 报 off 之前先问系统,记账里没有不等于没有"
```

---

## 完成后应当为真的判据

- 系统里有 Core 在跑时,`Down` 返回 `needs_attention` 而**不是** `off`,且拆除步骤全做完。
- 扫描失败时 `Down` **不返回 error**,状态仍是「没能确认」。
- `Down` 的**两条**出口都过这一关。
- 干净机器仍然报 `off`(没有变成噪声源)。
- 本轮五个变异逐条转红。

## 真机验收(用户执行,不由 agent 跑)

1. 正常 `sudo bx down` → 仍显示已停止(**这条最要紧**:若真机上误报 needs_attention,
   说明 `looksLikeCore` 在关闭之后仍匹配到了什么,要看清是什么)。
2. `sudo bx run` 在另一个终端跑着 → `sudo bx down` 必须**不**报已停止。
3. 菜单栏 Turn Off 在同样情形下也应显示需注意 —— 它读的是同一个 `protection_state`,
   这一条正是 CLI 侧探针覆盖不到、而本改动要覆盖的。
