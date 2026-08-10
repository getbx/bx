# 阶段③a:只观察的调谐器 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 Guardian 持续比对「用户要什么」与「系统实际是什么」,**把差异记下来并发布,但一个动作都不执行**——为阶段③b 的逐项授权攒真实证据。

**Architecture:** 判据抽成与平台无关的纯函数(沿用本仓库 `decideCoreScan`/`failoverPolicy.decide` 的既有手法);循环挂在 Guardian daemon 上,尊重既有的三道栅栏,**不持有 mutation channel 做观测**,只在差异**发生变化**时打日志。

**Tech Stack:** Go 1.26、既有的 `internal/observe`(`Observe`/`ObservedState`/`Tristate`)、`internal/guardian` 的 `DesiredState` 与栅栏。

## Global Constraints

- **本期一个动作都不许执行。** `decide` 的输出只进日志。这条由**行为断言**守住(注入的 fake 上,所有 mutating hook 调用次数为 0),不是源码文本匹配。
- **「观测不到」必须产出「什么都不做」**,绝不能按 `Unknown` 收敛。`observe.Tristate` 的零值就是 `Unknown`,这不是巧合而是设计——`internal/observe` 整个包是为这条写的。
- **循环绝不由自己去清 `m.current.Uncertain`。** 那是一个存在意义就是「拒绝」的 fail-closed 判断(`process.go:253-259`)。撞上它 = 本轮什么都不做。
- **三道栅栏,任一升起即整轮跳过**:`recoveryBlocked`(`manager.go:192`)、`pathRecoveryFences > 0`(`manager.go:211`)、`acquireMutation` 返回 `errMutationBusy`(`manager.go:54`)。**拿不到锁就安静跳过,绝不重试**——channel 唤醒是 FIFO 的,硬重试会把用户的 `up` 挤过 60s 预算变成 `guardian_busy`。
- **观测不许持有 mutation channel。** 一整轮观测在 darwin 上是 6 次进程 fork 加约 900 次 syscall。
- **日志只在差异变化时打**,不是每拍一行。五条常驻的「无法观测」会把这个字段训练成噪声——`cli/cli.go:4288-4295` 那道 darwin 门就是为此存在的。
- TDD;中文 conventional commits,结尾 `Co-Authored-By: Claude <noreply@anthropic.com>`;直接在 `master` 提交。
- 验证:`go build ./... && go vet ./... && go test ./... -count=1`;`go test -race ./internal/guardian -count=1`;交叉编译 linux/darwin/windows × amd64/arm64;`gofumpt -l`(排除 `internal/embedded/assets/` 与 `internal/winfw/`)。**自己跑 gofumpt 并读输出**——本项目已有三次「报告说格式干净」而 CI 会红。
- **agent 不得在宿主上跑 `bx`/`sudo bx`/`launchctl`/`route`/`networksetup`**;用户机器正开着 bx 日常使用。
- **集成台测不到这个循环**(Guardian 只在 darwin 跑,台子是 Linux netns 的)。凡是本计划写「由测试保证」的,对循环的**接线**而言是「由替身保证」——这正是本期不给它执行权的原因。

---

## Task 1: 纯判据

**Files:**
- Create: `internal/guardian/reconcile.go`
- Create: `internal/guardian/reconcile_test.go`

**Interfaces:**
- Produces:
  ```go
  type reconcileAction string

  const (
      actionNone              reconcileAction = ""
      actionRestoreDNS        reconcileAction = "restore_dns"
      actionClearOrphanBarrier reconcileAction = "clear_orphan_barrier"
      actionStartCore         reconcileAction = "start_core"
      actionStopCore          reconcileAction = "stop_core"
  )

  type reconcileInput struct {
      Desired  DesiredState
      Observed observe.ObservedState
      // 栅栏:任一为真即本轮什么都不做。分开列是为了日志能说清是哪一道。
      RecoveryBlocked  bool
      PathRecoveryBusy bool
      OwnershipUncertain bool
  }

  type reconcileDecision struct {
      Actions []reconcileAction
      // Held 非空时 Actions 必为空,内容是「因为哪道栅栏」。
      Held string
  }

  func decide(in reconcileInput) reconcileDecision
  ```

**动作是「命名的意图」,不是函数指针。** 这样日志读得懂,而③b 可以一项一项地授权。

- [ ] **Step 1: 写失败测试**

`internal/guardian/reconcile_test.go`:

```go
package guardian

import (
	"testing"

	"github.com/getbx/bx/internal/observe"
)

// 「观测不到」必须产出「什么都不做」。
//
// 这是整个调谐器最重要的一条,也是本仓库反复栽的那一条:Tristate 的零值是 Unknown
// 不是巧合 —— internal/observe 整个包就是为「观测不到 ≠ 观测到没有」写的。
// 按 Unknown 收敛意味着:一次 route 命令失败,调谐器就去「修」一个它根本没看见的问题。
func TestDecideDoesNothingWhenObservationIsUnknown(t *testing.T) {
	for _, desired := range []DesiredState{DesiredOn, DesiredOff} {
		got := decide(reconcileInput{Desired: desired}) // 全零 ObservedState = 全 Unknown
		if len(got.Actions) != 0 {
			t.Errorf("desired=%s 且什么都观测不到时必须无动作, got %v", desired, got.Actions)
		}
	}
}

// desired=off 而 DNS 仍归 bx:这是设计里点名「确实可删」的那一条补偿,
// 观测原语有、动作幂等、分歧规则已写好。
func TestDecideRestoresDNSWhenUserWantsOffButDNSIsStillManaged(t *testing.T) {
	got := decide(reconcileInput{
		Desired:  DesiredOff,
		Observed: observe.ObservedState{DNSManaged: observe.True},
	})
	if !hasAction(got.Actions, actionRestoreDNS) {
		t.Fatalf("desired=off 而 DNS 仍归 bx,应当提议还原, got %v", got.Actions)
	}
}

// 反过来:DNS 已经不归 bx 了就没什么可做 —— 否则每一拍都在提议一件已经成立的事。
func TestDecideProposesNothingWhenDNSAlreadyRestored(t *testing.T) {
	got := decide(reconcileInput{
		Desired:  DesiredOff,
		Observed: observe.ObservedState{DNSManaged: observe.False},
	})
	if hasAction(got.Actions, actionRestoreDNS) {
		t.Fatalf("DNS 已还原,不该再提议, got %v", got.Actions)
	}
}

// desired=off 而屏障还在:孤儿屏障。**只提议「删」,永远不提议「装」** ——
// 装屏障是整个系统里唯一一个失败后用户连修复包都下不了的动作,
// 它绝不能出现在任何按时钟驱动的判据里。
func TestDecideNeverProposesInstallingABarrier(t *testing.T) {
	for _, in := range []reconcileInput{
		{Desired: DesiredOn, Observed: observe.ObservedState{BarrierPresent: observe.False}},
		{Desired: DesiredOn, Observed: observe.ObservedState{CaptureOK: observe.False}},
		{Desired: DesiredOff, Observed: observe.ObservedState{BarrierPresent: observe.True}},
	} {
		for _, action := range decide(in).Actions {
			if action == "install_barrier" {
				t.Fatalf("绝不能提议装屏障:一次瞬时的网关探测失败会让它降级成 block-only"+
					"(无 server bypass)的整机黑洞,而循环每拍都在重掷这个骰子。input=%+v", in)
			}
		}
	}
}

func TestDecideProposesClearingAnOrphanBarrier(t *testing.T) {
	got := decide(reconcileInput{
		Desired:  DesiredOff,
		Observed: observe.ObservedState{BarrierPresent: observe.True},
	})
	if !hasAction(got.Actions, actionClearOrphanBarrier) {
		t.Fatalf("desired=off 而屏障还在,应当提议清掉, got %v", got.Actions)
	}
}

// 三道栅栏各自都要让整轮停摆,且要说清是哪一道。
func TestDecideHoldsOnEveryFence(t *testing.T) {
	base := observe.ObservedState{DNSManaged: observe.True} // 本来会提议还原 DNS
	for _, tc := range []struct {
		name string
		in   reconcileInput
	}{
		{"recoveryBlocked", reconcileInput{Desired: DesiredOff, Observed: base, RecoveryBlocked: true}},
		{"pathRecovery", reconcileInput{Desired: DesiredOff, Observed: base, PathRecoveryBusy: true}},
		{"uncertain", reconcileInput{Desired: DesiredOff, Observed: base, OwnershipUncertain: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := decide(tc.in)
			if len(got.Actions) != 0 {
				t.Errorf("%s 升起时必须整轮不做事, got %v", tc.name, got.Actions)
			}
			if got.Held == "" {
				t.Error("必须说清是被哪道栅栏挡住的 —— 否则日志里只会看到「什么都没发生」")
			}
		})
	}
}

// desired=on 而 Core 没在跑:**提议**起它。本期只是提议;
// 真授权要等 Uncertain 锁存有解(见设计),否则一次瞬时扫描失败会变成永久拒绝。
func TestDecideProposesStartingCoreWhenProtectionIsWantedButAbsent(t *testing.T) {
	got := decide(reconcileInput{
		Desired:  DesiredOn,
		Observed: observe.ObservedState{CoreSocket: observe.False},
	})
	if !hasAction(got.Actions, actionStartCore) {
		t.Fatalf("desired=on 而 Core 不在,应当提议起它, got %v", got.Actions)
	}
}

func hasAction(actions []reconcileAction, want reconcileAction) bool {
	for _, a := range actions {
		if a == want {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: 跑它,确认失败**

Run: `go test ./internal/guardian -run TestDecide -count=1`
Expected: FAIL,`undefined: decide`

- [ ] **Step 3: 实现**

`reconcile.go`。栅栏在**最前面**判,任一为真即 `return reconcileDecision{Held: …}`。
其后每一条规则都必须先确认对应的观测**不是 Unknown**。

**注释里要写明三件事**:动作为什么是命名意图而不是函数指针;为什么没有
`install_barrier` 这个动作(不是忘了,是刻意不存在);以及为什么 `Uncertain` 是栅栏
而不是「要收敛的差异」。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./... -count=1 && go test -race ./internal/guardian -count=1`

- [ ] **Step 5: 变异验证**

① 把栅栏判断挪到规则**之后** → `TestDecideHoldsOnEveryFence` 必须红。
② 把 DNS 规则的 `== observe.True` 改成 `!= observe.False` → `TestDecideDoesNothingWhenObservationIsUnknown` 必须红。
③ 加一个 `install_barrier` 动作并在 `desired=on && BarrierPresent==False` 时产出 → `TestDecideNeverProposesInstallingABarrier` 必须红。
三处改回。

- [ ] **Step 6: 提交**

```bash
git add internal/guardian/reconcile.go internal/guardian/reconcile_test.go
git commit -m "feat(guardian): 调谐判据抽成纯函数 —— 观测不到就什么都不做"
```

---

## Task 2: 只观察的循环

**Files:**
- Create: `internal/guardian/reconcile_loop.go`
- Create: `internal/guardian/reconcile_loop_test.go`
- Modify: `internal/guardian/daemon.go`(挂上循环)

**Interfaces:**
- Consumes: Task 1 的 `decide`
- Produces:
  - `func nextReconcileInterval(unchangedRounds int) time.Duration` —— 纯函数,可测
  - `func (m *Manager) reconcileOnce(ctx context.Context, observed observe.ObservedState) reconcileDecision` —— **只读**:读栅栏、调 `decide`、返回;**不执行任何动作**

- [ ] **Step 1: 写失败测试**

```go
// **本期的核心约束:一个动作都不许执行。**
//
// 判据会提议 restore_dns / clear_orphan_barrier / start_core,而本期它们只该进日志。
// 这条用行为断言守 —— 注入的 fake 上所有 mutating hook 调用次数必须为 0 ——
// 而不是读源码文本:本仓库的文本守卫这一轮被绕过了八次。
func TestReconcileOnceExecutesNothing(t *testing.T) {
	env := newManagerTestEnv(t)
	before := env.mutationCallCounts()

	got := env.manager.reconcileOnce(context.Background(), observe.ObservedState{
		DNSManaged:     observe.True,
		BarrierPresent: observe.True,
		CoreSocket:     observe.False,
	})
	if len(got.Actions) == 0 {
		t.Fatal("这份观测本该提议动作,否则这条测试证明不了「提议了但没执行」")
	}
	if after := env.mutationCallCounts(); after != before {
		t.Fatalf("只观察阶段绝不许执行任何动作\nbefore=%+v\nafter =%+v", before, after)
	}
}

// 观测连续相同就退避:一整轮观测在 darwin 上是 6 次 fork 加约 900 次 syscall,
// 按拍烧掉它没有意义,而且会让日志变成噪声。
func TestNextReconcileIntervalBacksOffWhenNothingChanges(t *testing.T) {
	first := nextReconcileInterval(0)
	if first < 30*time.Second {
		t.Fatalf("基础周期不该短于 30s(一轮观测 6 次 fork), got %v", first)
	}
	if nextReconcileInterval(10) <= first {
		t.Error("连续无变化时必须退避")
	}
	if got := nextReconcileInterval(1000); got > 10*time.Minute {
		t.Errorf("退避要有上限,否则真出事时它已经睡死了, got %v", got)
	}
}

// 拿不到 mutation channel 就安静跳过 —— 绝不重试。
// channel 唤醒是 FIFO 的,硬重试的循环会把用户的 up 挤过 60s 预算变成 guardian_busy。
func TestReconcileOnceYieldsSilentlyWhenMutationsAreBusy(t *testing.T) { /* … */ }
```

- [ ] **Step 2: 跑它,确认失败**

- [ ] **Step 3: 实现**

`reconcileOnce`:读三道栅栏 → 组 `reconcileInput` → `decide` → 返回。**不碰任何 mutating hook。**

循环:`daemon.go` 里照 `trackStartupRecovery` 的形状起一条 goroutine,**带 `recover()`**
(理由与 `75feb1f` 相同:这条 goroutine 的 panic 会打死 Guardian,而 launchd 的 KeepAlive
会把它拉起来再 panic —— 崩溃循环)。

日志:**只在 `reconcileDecision` 与上一轮不同时**打一行
`guardian_reconcile_would actions=… held=… observed=…`。相同就静默并累加退避计数。

- [ ] **Step 4: 跑全量**

- [ ] **Step 5: 变异验证**

① 让 `reconcileOnce` 真去调 `restoreDNS` → `TestReconcileOnceExecutesNothing` 必须红。
② 去掉退避(恒返回基础周期)→ 退避那条必须红。
③ 去掉 goroutine 的 `recover()` → 补一条与 `TestStartupRecoveryGoroutineSurvivesAPanic`
同形的测试,必须红。

- [ ] **Step 6: 提交**

```bash
git add internal/guardian/
git commit -m "feat(guardian): 只观察的调谐循环 —— 记下它本来会做什么,但什么都不做"
```

---

## Task 3: 把循环的判断发布出去(设计里③a 的第三样交付)

**这一项是写计划时漏的,不是 brief 的问题。** 设计
(`2026-08-09-stage3-reconciler-design.md:80-81`)给阶段③a 列了**三样**交付,计划只写了
两样;第三样是「`bx status` 的 divergence 从『一次快照』变成『持续观察』—— 今天那个
字段只在用户敲命令的那一刻算一次」。

**对「变成持续观察」的解读,以及为什么不是替换。** 既有的一次性观测**保留**:
`bx status` 是出问题时最先敲的命令,那一刻的新鲜事实正是它的价值,拿一份最多可能
陈旧 10 分钟(退避上限)的缓存去顶替它是退化。改为**并列发布**循环的最近一轮判断 ——
与本项目既有的「不用观测覆盖信念、二者的 diff 本身才是信号」同一条纪律。

**这一项直接服务 soak。** 今天要回答「循环提议过什么」只能 `sudo tail /var/log/bx-guard.log`
(root-only)。发布之后 `bx status` 就能回答,而 soak 是本阶段唯一真正的验收。

**Files:**
- Modify: `internal/guardian/types.go`(`ReconcileReport` 类型、`Status.Reconcile` 字段、能力声明)
- Modify: `internal/guardian/manager.go`(`recordReconcileRound` + `Status()` 附上)
- Modify: `internal/guardian/reconcile_loop.go`(每轮记一次)
- Modify: `internal/cli/cli.go`(`clientStatusReport` 加字段 + 渲染一行)
- Test: `internal/guardian/reconcile_report_test.go`、`internal/cli/cli_test.go`

**Interfaces:**
- Consumes: Task 1 的 `reconcileDecision`;Task 2 的 `runReconcileLoopWithPacing`
- Produces:
  - `type ReconcileReport struct { At time.Time; Actions []string; Held string; UnchangedRounds int }`
  - `func (m *Manager) recordReconcileRound(decision reconcileDecision, unchanged int)`
  - `Status.Reconcile *ReconcileReport`(`json:"reconcile,omitempty"`)
  - 能力串常量 `CapabilityReconcileReport = "reconcile-report"`

### 这一项唯一真正危险的地方

**零值读起来像「一切正常」。** `reconcileDecision` 的零值(无动作、无栅栏)正是**一台
健康机器**的判断 —— Task 2 的 `previous` 初值就是靠这一点让健康机器保持静默的。于是:

- 循环**从没跑过一轮**(没挂上、刚启动、每轮都 panic 被 recover 掉)
- 循环**跑了、什么差异都没有**

这两件事的判断值**完全相同**。而它们在真机上的意义正好相反,后者是 soak 想看到的结果,
前者是「你拿到的那份干净日志毫无意义」。这与 Task 2 修复轮 2 抓到的是**同一个洞**
(一个装了但没跑的循环,与一台健康机器,产出的日志一模一样)。

**处置**:`At` 是唯一的判据 —— 没跑过就是零时刻,此时 `Status.Reconcile` **为 nil、
字段整个缺席**;CLI 见 nil 时必须渲染「尚未完成第一轮观测」,**绝不能渲染成无差异**。
且渲染出的那一行**在无差异时也要出现**(带「最近观测于 N 前」),否则用户依旧分不清
「循环活着且干净」与「循环死了」—— 静默正是这一项要消灭的歧义。

- [ ] **Step 1: 写失败测试**

```go
// 零值绝不能读成「一切正常」。
//
// reconcileDecision 的零值就是一台健康机器的判断,于是「从没跑过一轮」与「跑了、
// 什么差异都没有」在判断值上完全相同 —— 而它们在真机上的意义正好相反。
// At 是唯一分得开二者的东西。
func TestStatusOmitsReconcileReportUntilARoundHasCompleted(t *testing.T) {
	env := newManagerTestEnv(t)
	if got := env.manager.Status().Reconcile; got != nil {
		t.Fatalf("一轮都没跑过时必须整个字段缺席,而不是发布一份「无差异」, got %+v", got)
	}
}

// 跑过之后必须发布,且「无差异」也要发布 —— 用户要分得清「循环活着且干净」
// 与「循环死了」,而后者正是 Task 2 修复轮 2 抓到的那个洞。
func TestStatusPublishesACleanReconcileRound(t *testing.T) {
	env := newManagerTestEnv(t)
	env.manager.recordReconcileRound(reconcileDecision{}, 7)
	got := env.manager.Status().Reconcile
	if got == nil {
		t.Fatal("跑过一轮之后必须发布,哪怕这一轮什么差异都没有")
	}
	if got.At.IsZero() {
		t.Error("At 是「跑过没跑过」的唯一判据,不能是零值")
	}
	if len(got.Actions) != 0 || got.Held != "" {
		t.Errorf("这一轮是干净的, got %+v", got)
	}
	if got.UnchangedRounds != 7 {
		t.Errorf("UnchangedRounds = %d, want 7", got.UnchangedRounds)
	}
}

// 报告不许被别的写 status 的路径顺手抹掉:它存在别的字段里,Status() 组装时附上。
// 变异:把它并进 m.status 结构体 —— 任何一处整体重建 status 的路径都会让这条红。
func TestReconcileReportSurvivesAStatusRebuild(t *testing.T) {
	env := newManagerTestEnv(t)
	env.manager.recordReconcileRound(reconcileDecision{Held: heldOwnershipUncertain}, 0)
	env.manager.needsAttention(DesiredOn, "core_scan_failed")
	got := env.manager.Status().Reconcile
	if got == nil || got.Held != heldOwnershipUncertain {
		t.Fatalf("别的路径重建 status 之后报告丢了, got %+v", got)
	}
}

// 循环每一轮都要记 —— 包括被栅栏挡住的那些轮:soak 要数的正是「有多少轮根本没在工作」。
func TestReconcileLoopRecordsEveryRoundIncludingHeldOnes(t *testing.T) { /* 用 Task 2 的
	runReconcileLoopWithPacing + 注入的假观测跑固定轮数,断言每轮之后 Status().Reconcile
	都更新过(At 前进),且被栅栏挡住的那轮 Held 非空。 */ }
```

CLI 侧:

```go
// nil 必须渲染成「尚未观测」,绝不能渲染成无差异 —— 这是本任务唯一真正危险的地方。
func TestRenderClientStatusDistinguishesNeverRanFromClean(t *testing.T) {
	never := renderClientStatus(clientStatusReport{ProtectionState: "protected"})
	clean := renderClientStatus(clientStatusReport{
		ProtectionState: "protected",
		Reconcile:       &guardian.ReconcileReport{At: time.Now().Add(-12 * time.Second), UnchangedRounds: 37},
	})
	if never == clean {
		t.Fatal("「从没跑过一轮」与「跑了、无差异」渲染成了同一段文字")
	}
	if !strings.Contains(clean, "无差异") {
		t.Errorf("无差异时也必须出现这一行,否则分不清循环活着还是死了:\n%s", clean)
	}
}

// 旧 Guardian(没声明这个能力)不该凭空多出一行 —— 与 observerForPlatform 同一条纪律:
// 字段缺席是诚实的「没问」。
func TestRenderClientStatusSaysNothingWhenGuardianHasNoReconcileLoop(t *testing.T) { /* … */ }
```

- [ ] **Step 2: 跑它,确认失败**

- [ ] **Step 3: 实现**

`Manager` 用**独立字段** `reconcileReport *ReconcileReport` 存(**不并进 `m.status`**:
有别的路径整体重建 status,并进去迟早被抹掉),`statusMu` 写保护 —— **不是 mutation
channel**,Global Constraint 明写观测不许持有它。`Status()` 读锁里附上。
`recordReconcileRound` 由 Task 2 循环在每轮判断之后调用,包括被栅栏挡住的轮。

CLI:`clientStatusReport` 加 `Reconcile *guardian.ReconcileReport`,`renderClientStatus`
按上面三态渲染(nil 且 Guardian 声明了能力 → 「尚未完成第一轮观测」;nil 且没声明 →
不输出;非 nil → 「调谐环 最近观测 N 前 · 无差异(连续 M 轮未变)/ 本会提议 … / 被 … 挡住」)。

- [ ] **Step 4: 跑全量**

- [ ] **Step 5: 变异验证**

① `recordReconcileRound` 里不写 `At` → 「零值不许读成正常」那条必须红。
② 把报告并进 `m.status` → 「重建后仍在」那条必须红。
③ CLI 对 nil 渲染成无差异 → 「分得开」那条必须红。
④ 循环只在有差异时才记 → 「每轮都记」那条必须红。

- [ ] **Step 6: 提交**

```bash
git add internal/guardian/ internal/cli/
git commit -m "feat(status): 并列发布调谐环的最近一轮判断 —— 让 soak 不必去翻 root 日志"
```

---

## 完成后应当为真的判据

- `decide` 在**任何** Unknown 观测上产出零动作。
- `decide` **永不**产出装屏障的动作(该动作根本不存在于类型里)。
- 三道栅栏各自都能让整轮停摆,且日志说得清是哪一道。
- 循环跑一轮之后,所有 mutating hook 的调用次数为 **0**(行为断言)。
- 观测连续相同时周期退避,且退避有上限。

## 真机 soak(用户执行,这是本阶段唯一真正的验收)

开着只观察的循环跑一天,然后回答两个问题:

1. **`guardian_reconcile_would` 出现了几次、内容是什么?** 一台健康机器上理想是 **0 次**。
   若持续提议某个动作,说明判据或观测有问题 —— **而这正是本期存在的理由:在授权之前
   把它测出来。**
2. **`looksLikeCore` 的误报率。** 它按 argv 判定、刻意不匹配可执行路径
   (`2026-08-09-down-must-not-lie-design.md:107-111` 标记为**从未测量**)。日志里
   `core_still_running` 或 `start_core` 的异常出现频率就是答案。

**在拿到这两个数字之前,阶段③b 一项授权都不该给。**
