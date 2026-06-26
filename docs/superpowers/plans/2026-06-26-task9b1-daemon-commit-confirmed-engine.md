# Task 9b-1 — 守护进程 commit-confirmed 引擎 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在 `internal/supervisor` 实现一个常驻守护进程的 commit-confirmed 引擎单元——`confirm.Guard`(死手)+ `confirm.Snapshotter`(9a 真快照器)+ tickLoop 薄编排,`Arm(apply,undo)`/`Commit`/`Rollback`/`Run`;纯编排免-root 单测 + netns 真快照器往返。

**Architecture:** 把 MCP 短命进程里的 `armThen`(`internal/mcp/tools_mutating.go`)搬进 supervisor 包、换成真快照器、加 undo 钩子。新代码只有薄编排 + tickLoop,死手状态机复用 `confirm.Guard`。**不挂 Run() 守护循环、不接 socket/MCP/真隧道 mutation**(9b-2/9b-3)。

**Tech Stack:** Go 1.26.3;复用 `internal/confirm`(Guard/Snapshotter,已纯测 + CI 真机验)、`internal/supervisor` 现有 `NewSystemSnapshotter()`(9a)。

## Global Constraints

- 模块 `github.com/getbx/bx`,Go 1.26.3。
- TDD:失败测试→红→实现→绿→提交。纯编排测试免 root,在 macOS 原生跑(无 build tag)。
- 提交信息中文 conventional + 结尾 `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`。在 master 直接提交。
- **不接生产 mutation / 不挂 Run() / 不接 socket / 不接 MCP**:9b-1 只交付引擎单元 + 测试。9a 快照器在引擎死手里首次 live,但只围绕 netns 合成 mutation,brick-safe。
- 死手窗口默认 240s,时钟可注入。
- 复用的 `confirm` API(已存在,见 `internal/confirm/deadman.go` / `snapshot.go`):`confirm.New(window time.Duration, now func() time.Time) *Guard`;`(*Guard).Arm(restore func() error) error`(ErrAlreadyArmed)、`.Commit() error`/`.Rollback() error`(ErrNotArmed)、`.Tick() (bool, error)`、`.State() confirm.State`;`confirm.StateIdle/StateArmed/StateCommitted/StateReverted`;`confirm.ErrNotArmed`;`confirm.Snapshotter{ Capture() (Snapshot, error); Restore(Snapshot) error }`、`confirm.Snapshot{ ID() string }`。
- netns 验证基建已就绪(`unshare(CLONE_NEWNET)`+`LockOSThread`+dummy;CI `integration` job + SKIP 守卫);本机 macOS 跑不了 netns 测,真跑在 CI/Colima。

---

### Task 1: 引擎单元 + 纯编排测试(Mac 原生)

**Files:**
- Create: `internal/supervisor/mutationengine.go`(无 build tag)
- Create: `internal/supervisor/mutationengine_test.go`(无 build tag)

**Interfaces:**
- Produces:
  - `type mutationEngine struct { guard *confirm.Guard; snapper confirm.Snapshotter }`
  - `func newMutationEngine(snapper confirm.Snapshotter, window time.Duration, now func() time.Time) *mutationEngine`
  - `func (e *mutationEngine) Arm(apply, undo func() error) error`
  - `func (e *mutationEngine) Commit() error` / `Rollback() error` / `State() confirm.State` / `tick() (bool, error)`
  - `func (e *mutationEngine) Run(ctx context.Context)`
- Consumes: `internal/confirm`(Guard/Snapshotter/Snapshot/State/errors,已存在)。

- [ ] **Step 1: 写失败测试**

Create `internal/supervisor/mutationengine_test.go`:
```go
package supervisor

import (
	"errors"
	"testing"
	"time"

	"github.com/getbx/bx/internal/confirm"
)

type engFakeSnap struct{}

func (engFakeSnap) ID() string { return "fake" }

type engFakeSnapper struct {
	captureErr error
	captures   int
	restores   int
}

func (s *engFakeSnapper) Capture() (confirm.Snapshot, error) {
	s.captures++
	if s.captureErr != nil {
		return nil, s.captureErr
	}
	return engFakeSnap{}, nil
}
func (s *engFakeSnapper) Restore(confirm.Snapshot) error { s.restores++; return nil }

type engClock struct{ t time.Time }

func (c *engClock) now() time.Time { return c.t }

func newTestEngine(snapper confirm.Snapshotter, clk *engClock) *mutationEngine {
	return newMutationEngine(snapper, 240*time.Second, clk.now)
}

func TestEngineArmCaptureFailDoesNotApply(t *testing.T) {
	snapper := &engFakeSnapper{captureErr: errors.New("boom")}
	e := newTestEngine(snapper, &engClock{t: time.Unix(0, 0)})
	applied := false
	err := e.Arm(func() error { applied = true; return nil }, nil)
	if err == nil {
		t.Fatal("capture 失败应返回错误")
	}
	if applied {
		t.Fatal("capture 失败不应调用 apply")
	}
	if e.State() != confirm.StateIdle {
		t.Fatalf("应保持 Idle,得 %v", e.State())
	}
}

func TestEngineArmApplyFailReverts(t *testing.T) {
	snapper := &engFakeSnapper{}
	e := newTestEngine(snapper, &engClock{t: time.Unix(0, 0)})
	undoCalled := false
	err := e.Arm(
		func() error { return errors.New("apply boom") },
		func() error { undoCalled = true; return nil },
	)
	if err == nil {
		t.Fatal("apply 失败应返回错误")
	}
	if !undoCalled {
		t.Fatal("apply 失败应调用 undo")
	}
	if snapper.restores != 1 {
		t.Fatalf("apply 失败应调用快照 Restore 一次,得 %d", snapper.restores)
	}
	if e.State() != confirm.StateReverted {
		t.Fatalf("应 Reverted,得 %v", e.State())
	}
}

func TestEngineArmCommitDisarms(t *testing.T) {
	snapper := &engFakeSnapper{}
	clk := &engClock{t: time.Unix(0, 0)}
	e := newTestEngine(snapper, clk)
	if err := e.Arm(func() error { return nil }, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if e.State() != confirm.StateArmed {
		t.Fatalf("apply 成功应 Armed,得 %v", e.State())
	}
	if err := e.Commit(); err != nil {
		t.Fatal(err)
	}
	clk.t = clk.t.Add(300 * time.Second)
	if rev, _ := e.tick(); rev {
		t.Fatal("已 commit 不应回滚")
	}
	if snapper.restores != 0 {
		t.Fatalf("已 commit 不应 Restore,得 %d", snapper.restores)
	}
	if e.State() != confirm.StateCommitted {
		t.Fatalf("应 Committed,得 %v", e.State())
	}
}

func TestEngineNoCommitAutoReverts(t *testing.T) {
	snapper := &engFakeSnapper{}
	clk := &engClock{t: time.Unix(0, 0)}
	e := newTestEngine(snapper, clk)
	undoCalled := false
	if err := e.Arm(func() error { return nil }, func() error { undoCalled = true; return nil }); err != nil {
		t.Fatal(err)
	}
	clk.t = clk.t.Add(241 * time.Second)
	rev, err := e.tick()
	if err != nil {
		t.Fatal(err)
	}
	if !rev {
		t.Fatal("未 commit 到点应自动回滚")
	}
	if !undoCalled || snapper.restores != 1 {
		t.Fatalf("回滚应调 undo+Restore(undo=%v restores=%d)", undoCalled, snapper.restores)
	}
	if e.State() != confirm.StateReverted {
		t.Fatalf("应 Reverted,得 %v", e.State())
	}
}

func TestEngineRollbackIdle(t *testing.T) {
	e := newTestEngine(&engFakeSnapper{}, &engClock{t: time.Unix(0, 0)})
	if err := e.Rollback(); !errors.Is(err, confirm.ErrNotArmed) {
		t.Fatalf("idle 回滚应 ErrNotArmed,得 %v", err)
	}
}
```

- [ ] **Step 2: 跑红**

Run: `go test ./internal/supervisor/ -run Engine -v`
Expected: 编译失败,`mutationEngine`/`newMutationEngine` undefined。

- [ ] **Step 3: 写实现**

Create `internal/supervisor/mutationengine.go`:
```go
// mutationengine.go 是常驻守护进程的 commit-confirmed 引擎:confirm.Guard(死手)+
// confirm.Snapshotter(9a 真快照器)的薄编排。改动类操作 Arm 后须在窗口内 Commit,
// 否则 tickLoop 到点自动 revert(undo + 路由快照网)。把 MCP 短命进程里的 armThen
// 搬进守护进程,故 agent 断开后死手仍在。
//
// 9b-1 只交付引擎单元;挂进 Run() 守护循环 / 接控制 socket / 接真实隧道 mutation 是 9b-2/9b-3。
package supervisor

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/getbx/bx/internal/confirm"
)

type mutationEngine struct {
	guard   *confirm.Guard
	snapper confirm.Snapshotter
}

// newMutationEngine 构造引擎。生产用 NewSystemSnapshotter()+240s+time.Now;测试注入 fake。
func newMutationEngine(snapper confirm.Snapshotter, window time.Duration, now func() time.Time) *mutationEngine {
	return &mutationEngine{guard: confirm.New(window, now), snapper: snapper}
}

// Arm:抓快照 → 武装死手(restore = undo + 快照网)→ apply。
// capture 失败 → 不武装、不 apply;apply 失败 → 立即 Rollback、返回错误(不留半截)。
func (e *mutationEngine) Arm(apply, undo func() error) error {
	snap, err := e.snapper.Capture()
	if err != nil {
		return fmt.Errorf("抓 last-known-good 快照失败,已中止改动: %w", err)
	}
	restore := func() error {
		var errs []error
		if undo != nil {
			if uerr := undo(); uerr != nil {
				errs = append(errs, fmt.Errorf("undo: %w", uerr))
			}
		}
		if rerr := e.snapper.Restore(snap); rerr != nil {
			errs = append(errs, fmt.Errorf("快照还原: %w", rerr))
		}
		return errors.Join(errs...)
	}
	if err := e.guard.Arm(restore); err != nil {
		return err // ErrAlreadyArmed
	}
	if err := apply(); err != nil {
		_ = e.guard.Rollback() // apply 失败 → revert,不留半截
		return fmt.Errorf("apply 失败已回滚: %w", err)
	}
	return nil
}

func (e *mutationEngine) Commit() error       { return e.guard.Commit() }
func (e *mutationEngine) Rollback() error      { return e.guard.Rollback() }
func (e *mutationEngine) State() confirm.State { return e.guard.State() }
func (e *mutationEngine) tick() (bool, error)  { return e.guard.Tick() }

// Run 跑 tickLoop:每 2s Tick,未 commit 到点自动 revert,直到 ctx 取消。
func (e *mutationEngine) Run(ctx context.Context) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_, _ = e.tick()
		}
	}
}
```

- [ ] **Step 4: 跑绿**

Run:
```bash
go test ./internal/supervisor/ -run Engine -v
go test ./internal/supervisor/ && go vet ./internal/supervisor/ && go build ./...
```
Expected: 5 个 Engine 测试 PASS;全 supervisor 套件绿;build 绿。

- [ ] **Step 5: 提交**

```bash
git add internal/supervisor/mutationengine.go internal/supervisor/mutationengine_test.go
git commit -m "feat(supervisor): 守护进程 commit-confirmed 引擎(Guard+真快照器薄编排)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: netns 真快照器往返集成测试(`//go:build integration && linux`)

**Files:**
- Create: `internal/supervisor/mutationengine_netns_linux_test.go`(`//go:build integration && linux`)

**Interfaces:**
- Consumes: `newMutationEngine`(Task 1)、`NewSystemSnapshotter()`(9a)、netns 模式(`unshare`+`LockOSThread`+dummy)。

- [ ] **Step 1: 写测试**

Create `internal/supervisor/mutationengine_netns_linux_test.go`:
```go
//go:build integration && linux

package supervisor

import (
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// TestMutationEngineNetnsAutoRevert:引擎接真 NewSystemSnapshotter(),在 netns 内
// Arm 一个合成路由 mutation(加一条 ip rule)→ 不 commit → 推进时钟 + tick →
// 断言 ip rule 真机回到 arm 前基线。证明死手在守护进程引擎里、用真快照器、能自动回滚。
func TestMutationEngineNetnsAutoRevert(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("需要 root(Colima VM 或 CI sudo)")
	}
	if _, err := exec.LookPath("ip"); err != nil {
		t.Skip("缺 ip 命令")
	}
	runtime.LockOSThread() // 不 Unlock:goroutine 结束销毁线程,临时 netns 随之消失
	if err := unix.Unshare(unix.CLONE_NEWNET); err != nil {
		t.Skipf("unshare(CLONE_NEWNET) 失败: %v", err)
	}
	must := func(args ...string) {
		if out, err := exec.Command("ip", args...).CombinedOutput(); err != nil {
			t.Fatalf("ip %v: %v\n%s", args, err, out)
		}
	}
	ruleList := func() string {
		out, err := exec.Command("ip", "rule", "list").CombinedOutput()
		if err != nil {
			t.Fatalf("ip rule list: %v\n%s", err, out)
		}
		return string(out)
	}
	must("link", "set", "lo", "up")

	clk := &engClock{t: time.Unix(0, 0)}
	e := newMutationEngine(NewSystemSnapshotter(), 240*time.Second, clk.now)

	base := ruleList()
	// 合成 mutation:加一条独特的 ip rule;undo 为 nil(靠快照网删掉)。
	apply := func() error {
		return exec.Command("ip", "rule", "add", "pref", "12345", "table", "main").Run()
	}
	if err := e.Arm(apply, nil); err != nil {
		t.Fatalf("Arm: %v", err)
	}
	if ruleList() == base {
		t.Fatal("Arm 后规则应已变(测试前提不成立)")
	}

	// 不 commit,推进时钟过窗口,tick → 自动 revert。
	clk.t = clk.t.Add(241 * time.Second)
	rev, err := e.tick()
	if err != nil {
		t.Fatalf("tick revert 报错: %v", err)
	}
	if !rev {
		t.Fatal("未 commit 到点应自动 revert")
	}
	if got := ruleList(); got != base {
		t.Fatalf("revert 未回到基线:\n--- base ---\n%s\n--- got ---\n%s", base, got)
	}
}
```

- [ ] **Step 2: 本机门控验证**

Run:
```bash
cd /Users/nategu_mac_company/Documents/bx
GOOS=linux go vet -tags integration ./internal/supervisor/
go test ./internal/supervisor/ 2>&1 | tail -3
```
Expected: `GOOS=linux go vet -tags integration` 干净(无 helper 冲突:本测试用局部 `must`/`ruleList`,复用 Task 1 的 `engClock`);Mac 上 `go test`(无 tag)照常、不含本测试。**本机 macOS 跑不了真测**,真跑在 CI/Colima。

- [ ] **Step 3:(可选)真机实跑**

如有 Linux:`sudo "$(which go)" test -tags integration -run TestMutationEngineNetnsAutoRevert ./internal/supervisor/ -v` → Expected PASS。报告注明本机未跑。

- [ ] **Step 4: 提交**

```bash
git add internal/supervisor/mutationengine_netns_linux_test.go
git commit -m "test(supervisor): 引擎 netns 真快照器自动回滚集成测

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review(对照 spec)

**Spec 覆盖:**
- 引擎单元(位置/构造/API Arm(apply,undo)/Commit/Rollback/State/Run)→ Task 1。
- revert = undo + 快照网 → Task 1 `Arm` 的 restore 闭包。
- capture 失败不武装不 apply / apply 失败立即 revert → Task 1 实现 + `TestEngineArmCaptureFailDoesNotApply`/`TestEngineArmApplyFailReverts`。
- Commit 解除 / 未 commit Tick 自动 revert / Rollback 透传 ErrNotArmed → Task 1 三个对应测试。
- 与 `--test-timeout` 并存(独立机制)→ 不动 run.go,引擎是独立单元(Global Constraints + 不挂 Run())。
- 测试分层(fakeSnapshotter+fake时钟纯测 + netns 真快照器往返)→ Task 1 纯测、Task 2 netns。
- 不接生产 mutation / 不挂 Run()/socket/MCP → Global Constraints + 仅新增引擎单元与测试。

**占位扫描:** 无 TBD;每段代码完整。Task 2 本机不跑真测、Step 3 可选真机——对 macOS 环境的诚实记录(靠 GOOS=linux vet 编译 + CI/Colima 真跑),非占位。

**类型一致性:** `mutationEngine`/`newMutationEngine`/`Arm`/`Commit`/`Rollback`/`State`/`tick`/`Run`(T1)被 T2 一致引用;`engClock`/`engFakeSnapper`(T1 测试)被 T2 复用 `engClock`;复用的 `confirm.New`/`Guard` 方法/`StateXxx`/`ErrNotArmed`/`Snapshotter`、`NewSystemSnapshotter()`(9a)均与现状一致(已核对)。
