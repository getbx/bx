# 阶段③c:调谐环执行 `start_core` —— 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `desired=on` 而 Core 的控制 socket 不应答时,调谐环向系统求证没有 Core 在跑之后,用 `startCoreLocked` 把它起回来;每段故障最多 5 次,过了只观察。

**Architecture:** 一切落在既有的 ③b 执行器(`internal/guardian/reconcile_execute.go`)里:白名单加一项;槽内「前置条件」按动作(start_core 要 on,清理要 off);start_core 分支 = 槽内现扫 `ScanRunning` → 三态 → 计数 → `startCoreLocked`。`decide` 一字不改。计数器是 Manager 上的一个原子量,观测到 socket 应答或用户 `Up` 成功时归零。

**Tech Stack:** Go 1.26,`internal/guardian`(Manager、fakeCoreRunner 测试替身、`newManagerTestEnv`),`internal/cli`(`bx status` 渲染)。

**Spec:** `docs/superpowers/specs/2026-09-05-stage3c-start-core-design.md`

## Global Constraints

- TDD:每个任务先写失败测试、跑红、最小实现、跑绿、提交。变异验证靠 `bash scripts/verify.sh`(判据是退出码)。
- `decide`(`reconcile.go`)**一字不改**;「装屏障」永不进白名单;`stop_core` 不授权;循环不清 `m.current.Uncertain`。
- 起 Core **只经 `startCoreLocked`**,不新写任何起进程的代码。
- 发布面(`ReconcileExecution.Error`、`bx status`)只带稳定的码;原始错误只进 Guardian 日志(`log.Printf`)。
- 三态纪律:扫描「没测成」与「测到 0 个」严格分开,前者绝不允许起 Core。
- 被扫描拦下的两种(`core_process_present`/`core_scan_failed`)**不计入**次数;封顶 `maxReconcileStartCoreAttempts = 5`。
- `bx status` 里 `start_core_exhausted` 不许出现「让路」二字。
- 提交信息中文 conventional commits,结尾带 `Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>` 与 `Claude-Session: https://claude.ai/code/session_011KfnvatqtCZ5Gc8LDtJzU5`。直接提交默认分支。
- 测试免 root,用 `newManagerTestEnv(t)`(fakeCoreRunner:`startErr` 让 Start 失败、`scanResult`/`scanErr` 脚本扫描、`startCount()` 数 Start 次数、`currentProcess()` 取当前进程;`env.events.snapshot()` 取事件流,事件名 `barrier.install`/`barrier.release`/`barrier.remove`;`env.store.SaveDesired(DesiredOn|DesiredOff)` 改意图)。
- 跑单测:`go test ./internal/guardian/ -run '<名字>'`;全量闸门:`bash scripts/verify.sh`(改完 CLI 与 CLAUDE.md 之后)。

---

### Task 1: 白名单加 `start_core`,三个新码导出,守卫改名

**Files:**
- Modify: `internal/guardian/reconcile_execute.go:21-45`
- Modify: `internal/guardian/reconcile_execute_test.go:81-113`
- Modify: `CLAUDE.md:696`(那句点名旧测试名的话)

**Interfaces:**
- Produces:
  - `executableReconcileActions` 含 `actionStartCore`;
  - 导出常量 `ReconcileSkipCoreProcessPresent = "core_process_present"`、`ReconcileSkipCoreScanFailed = "core_scan_failed"`、`ReconcileSkipStartCoreExhausted = "start_core_exhausted"`(Task 3、Task 5 用);
  - `maxReconcileStartCoreAttempts = 5`(Task 3 用)。

- [ ] **Step 1: 改守卫为三项并改名(先红)**

把 `reconcile_execute_test.go` 里 `TestExecutableWhitelistIsExactlyTheOffCleanupPair` 整个替换为:

```go
// 白名单的内容本身要钉死:下面那条穷举测试只测「名单**外**的动作不执行」,
// 名单越大它测得越少。③c 把 start_core 加进来是有意识的决定(spec
// 2026-09-05-stage3c-start-core-design.md),stop_core 仍不在。
func TestExecutableWhitelistIsExactlyOffCleanupPlusStartCore(t *testing.T) {
	if len(executableReconcileActions) != 3 ||
		!executableReconcileActions[actionRestoreDNS] ||
		!executableReconcileActions[actionClearOrphanBarrier] ||
		!executableReconcileActions[actionStartCore] {
		t.Fatalf("执行白名单 = %v,扩它之前先读 ③c spec 的「不做」一节", executableReconcileActions)
	}
	if executableReconcileActions[actionStopCore] {
		t.Fatal("stop_core 不授权:desired=off + socket 应答最常见来源是 sudo bx run 调试路径")
	}
}
```

- [ ] **Step 2: 跑红**

Run: `go test ./internal/guardian/ -run 'TestExecutableWhitelistIsExactlyOffCleanupPlusStartCore'`
Expected: FAIL,`执行白名单 = map[clear_orphan_barrier:true restore_dns:true]`。

- [ ] **Step 3: 改白名单与常量**

在 `reconcile_execute.go` 把 `var executableReconcileActions` 改为:

```go
// 阶段③c 起 start_core 进名单:准入是槽内现扫 ScanRunning(测成 0 个才起),
// 起 Core 走 startCoreLocked(与 bx up 同一条原语),每段故障最多
// maxReconcileStartCoreAttempts 次。spec:2026-09-05-stage3c-start-core-design.md。
// stop_core 仍不授权:desired=off + socket 应答最常见的来源是 `sudo bx run`。
var executableReconcileActions = map[reconcileAction]bool{
	actionRestoreDNS:         true,
	actionClearOrphanBarrier: true,
	actionStartCore:          true,
}
```

并把文件头注释里「授权面**只有 desired=off 的两个清理动作**……」那段的第一句改成「授权面是 desired=off 的两个清理动作 + desired=on 的 start_core(③c)」,「观察态的两个」改成「观察态的一个(stop_core)」。

在 `const (` 块末尾追加:

```go
	// ③c start_core 的三个稳定码。导出:internal/cli 渲染用同一份名字,
	// 不许两边各抄一份字符串(与 internal/udpsource 那条纪律同源)。
	//   - core_process_present:扫到 ≥1 个 Core 进程,socket 却不应答 —— 卡住但
	//     活着,起第二个正是 af81632 双 Core 的入口,本期只显形不处置;
	//   - core_scan_failed:没测成。「问不出来」不是「没有」;
	//   - start_core_exhausted:本段故障已试满 maxReconcileStartCoreAttempts 次。
	ReconcileSkipCoreProcessPresent  = "core_process_present"
	ReconcileSkipCoreScanFailed      = "core_scan_failed"
	ReconcileSkipStartCoreExhausted  = "start_core_exhausted"
	// maxReconcileStartCoreAttempts 是每段故障(socket 首次不应答 → 再次应答)
	// 里起 Core 的上限。起进程不幂等:一份坏配置被每 30 秒起一次杀一次是敌意
	// 软件。5 没有真机依据,取保守值。
	maxReconcileStartCoreAttempts = 5
```

- [ ] **Step 4: 跑绿(整包)**

Run: `go test ./internal/guardian/`
Expected: PASS。`TestReconcileExecutionRefusesEveryUnauthorizedAction` 仍绿(它只喂名单外的动作,现在只剩 stop_core)。**注意**:此时 `start_core` 已在名单里但执行分支还没写,`executeUnderMutationSlot` 会因 `input.Desired != DesiredOff` 返回 `preconditions_changed` —— 这正是 Task 2 要改的,不影响本任务的绿。

- [ ] **Step 5: 改 CLAUDE.md 那一句**

`CLAUDE.md:696` 把 `` `TestExecutableWhitelistIsExactlyTheOffCleanupPair` 钉死 `` 改成 `` `TestExecutableWhitelistIsExactlyOffCleanupPlusStartCore` 钉死(③c 起含 start_core) ``。

Run: `go test ./internal/cli/ -run 'TestEveryTestNameMentionedInProseExists'`
Expected: PASS。

- [ ] **Step 6: 提交**

```bash
git add internal/guardian/reconcile_execute.go internal/guardian/reconcile_execute_test.go CLAUDE.md
git commit -m "feat(guardian): ③c 白名单加 start_core,三个稳定码导出,穷举守卫改名"
```

---

### Task 2: 槽内前置条件按动作 + start_core 的准入与执行

**Files:**
- Modify: `internal/guardian/reconcile_execute.go:82-133`(`executeUnderMutationSlot`)
- Test: `internal/guardian/reconcile_execute_test.go`(追加)

**Interfaces:**
- Consumes:`ReconcileSkipCoreProcessPresent`、`ReconcileSkipCoreScanFailed`(Task 1);`m.startCoreLocked(ctx)`、`m.runner.(coreScanner)`、`m.restartTimeout`(既有)。
- Produces:`requiredDesired(action reconcileAction) DesiredState`;`decideStartCoreAdmission(cores []Process, scanErr error, supported bool) (code string)`(空串 = 允许)。

- [ ] **Step 1: 写失败测试(四条)**

追加到 `reconcile_execute_test.go`:

```go
// ③c:start_core 的前置是 desired=on(清理动作要 off)。决策与拿到槽之间用户
// 刚好 down 了,按陈旧决策起一个用户刚关掉的 Core 是本文件最不可犯的错。
func TestStartCoreYieldsWhenDesiredFlippedToOff(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.store.SaveDesired(DesiredOff); err != nil {
		t.Fatal(err)
	}
	got := env.manager.executeReconcileAction(context.Background(), reconcileDecision{
		Actions: []reconcileAction{actionStartCore},
	})
	if got == nil || got.Outcome != reconcileExecutedSkipped || got.Error != reconcileSkipPreconditions {
		t.Fatalf("Executed = %+v, want skipped/preconditions_changed", got)
	}
	if env.runner.startCount() != 0 {
		t.Fatal("desired=off 却起了 Core")
	}
}

// 准入是扫描,不是 socket:测成 0 个才起,起的是 startCoreLocked 那条路。
func TestStartCoreStartsWhenScanFindsNoCore(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.store.SaveDesired(DesiredOn); err != nil {
		t.Fatal(err)
	}
	got := env.manager.executeReconcileAction(context.Background(), reconcileDecision{
		Actions: []reconcileAction{actionStartCore},
	})
	if got == nil || got.Action != string(actionStartCore) || got.Outcome != reconcileExecutedOK {
		t.Fatalf("Executed = %+v, want start_core/ok", got)
	}
	if env.runner.startCount() != 1 {
		t.Fatalf("Start 被调了 %d 次,want 1", env.runner.startCount())
	}
	if env.manager.Status().Protection != ProtectionProtected {
		t.Fatalf("起完不是 Protected: %q", env.manager.Status().Protection)
	}
}

// 扫到有 Core 进程(卡住但活着)⇒ 一个都不起,码 core_process_present。
func TestStartCoreRefusesWhenACoreProcessIsPresent(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.store.SaveDesired(DesiredOn); err != nil {
		t.Fatal(err)
	}
	env.runner.scanResult = []Process{{PID: 4242}}
	got := env.manager.executeReconcileAction(context.Background(), reconcileDecision{
		Actions: []reconcileAction{actionStartCore},
	})
	if got == nil || got.Outcome != reconcileExecutedSkipped || got.Error != ReconcileSkipCoreProcessPresent {
		t.Fatalf("Executed = %+v, want skipped/core_process_present", got)
	}
	if env.runner.startCount() != 0 {
		t.Fatal("有 Core 进程在跑还起了第二个 —— af81632 双 Core 的入口")
	}
}

// 没测成 ⇒ 不起,码 core_scan_failed。「问不出来」不是「没有」。
func TestStartCoreRefusesWhenScanFails(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.store.SaveDesired(DesiredOn); err != nil {
		t.Fatal(err)
	}
	env.runner.scanErr = errors.New("sysctl: EIO")
	got := env.manager.executeReconcileAction(context.Background(), reconcileDecision{
		Actions: []reconcileAction{actionStartCore},
	})
	if got == nil || got.Outcome != reconcileExecutedSkipped || got.Error != ReconcileSkipCoreScanFailed {
		t.Fatalf("Executed = %+v, want skipped/core_scan_failed", got)
	}
	if env.runner.startCount() != 0 {
		t.Fatal("扫描失败还起了 Core")
	}
}

// 纯判据:三态各自的码,supported=false 与 scanErr 同归「没测成」。
func TestDecideStartCoreAdmission(t *testing.T) {
	cases := []struct {
		name      string
		cores     []Process
		err       error
		supported bool
		want      string
	}{
		{"测成 0 个", nil, nil, true, ""},
		{"测成 1 个", []Process{{PID: 1}}, nil, true, ReconcileSkipCoreProcessPresent},
		{"扫描出错", nil, errors.New("x"), true, ReconcileSkipCoreScanFailed},
		{"runner 不会扫", nil, nil, false, ReconcileSkipCoreScanFailed},
		{"出错且有结果", []Process{{PID: 1}}, errors.New("x"), true, ReconcileSkipCoreScanFailed},
	}
	for _, c := range cases {
		if got := decideStartCoreAdmission(c.cores, c.err, c.supported); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}
```

- [ ] **Step 2: 跑红**

Run: `go test ./internal/guardian/ -run 'TestStartCore|TestDecideStartCoreAdmission'`
Expected: 编译失败(`decideStartCoreAdmission` 未定义);把那条测试暂时注释掉再跑,其余三条 start 类测试应 FAIL(全部落到 `preconditions_changed`,因为槽内还写死了 `desired==off`)。

- [ ] **Step 3: 实现**

在 `reconcile_execute.go` 追加:

```go
// requiredDesired 是每个动作要求的意图:清理要 off(用户明确说了关),start_core
// 要 on。此前写死 `desired==off`,那在只有清理动作时是对的,③c 起不再是。
func requiredDesired(action reconcileAction) DesiredState {
	if action == actionStartCore {
		return DesiredOn
	}
	return DesiredOff
}

// decideStartCoreAdmission 是 start_core 准入的**全部判定**,纯函数。
// 三态:测成 0 个 → 允许(空串);测成 ≥1 个 → core_process_present;没测成
// (runner 不会扫 / 扫描出错)→ core_scan_failed。出错时哪怕带着结果也算没测成 ——
// 「问不出来」永远不等于「没有」。
func decideStartCoreAdmission(cores []Process, scanErr error, supported bool) string {
	if !supported || scanErr != nil {
		return ReconcileSkipCoreScanFailed
	}
	if len(cores) > 0 {
		return ReconcileSkipCoreProcessPresent
	}
	return ""
}

// scanForStartCore 在槽内现扫一次。经 observingCoreScanner/coreScanner 与循环的
// 只读测量走同一个 runner 入口,单测替身、循环、准入三处看到的是同一份扫描。
func (m *Manager) scanForStartCore() (cores []Process, err error, supported bool) {
	if s, ok := m.runner.(observingCoreScanner); ok {
		cores, err = s.ScanRunningObserved()
		return cores, err, true
	}
	if s, ok := m.runner.(coreScanner); ok {
		cores, err = s.ScanRunning()
		return cores, err, true
	}
	return nil, nil, false
}
```

把 `executeUnderMutationSlot` 里这一段:

```go
	if input.Desired != DesiredOff {
		return &ReconcileExecution{Action: string(action), Outcome: reconcileExecutedSkipped, Error: reconcileSkipPreconditions}, ""
	}

	execCtx, cancelExec := context.WithTimeout(ctx, reconcileExecuteTimeout)
	defer cancelExec()
	var runErr error
	switch action {
```

改成:

```go
	if input.Desired != requiredDesired(action) {
		return &ReconcileExecution{Action: string(action), Outcome: reconcileExecutedSkipped, Error: reconcileSkipPreconditions}, ""
	}

	if action == actionStartCore {
		return m.executeStartCore(ctx)
	}

	execCtx, cancelExec := context.WithTimeout(ctx, reconcileExecuteTimeout)
	defer cancelExec()
	var runErr error
	switch action {
```

并追加:

```go
// executeStartCore 在槽内(调用方已持 mutation 槽、已复核栅栏与意图)起 Core。
// 顺序:现扫 → 三态 → startCoreLocked。超时取 restartTimeout 而不是
// reconcileExecuteTimeout:起 Core 要等健康,与 handleUnexpectedExit 那次重启
// 同一个预算。
func (m *Manager) executeStartCore(ctx context.Context) (*ReconcileExecution, string) {
	cores, scanErr, supported := m.scanForStartCore()
	if code := decideStartCoreAdmission(cores, scanErr, supported); code != "" {
		detail := ""
		if scanErr != nil {
			detail = scanErr.Error()
		} else if len(cores) > 0 {
			detail = fmt.Sprintf("cores=%d first_pid=%d", len(cores), cores[0].PID)
		}
		return &ReconcileExecution{Action: string(actionStartCore), Outcome: reconcileExecutedSkipped, Error: code}, detail
	}
	execCtx, cancelExec := context.WithTimeout(ctx, m.restartTimeout)
	defer cancelExec()
	if _, err := m.startCoreLocked(execCtx); err != nil {
		return &ReconcileExecution{Action: string(actionStartCore), Outcome: reconcileExecutedFailed, Error: reconcileExecuteFailedCode}, err.Error()
	}
	return &ReconcileExecution{Action: string(actionStartCore), Outcome: reconcileExecutedOK}, ""
}
```

`import` 加 `"fmt"`。

- [ ] **Step 4: 跑绿**

Run: `go test ./internal/guardian/ -run 'TestStartCore|TestDecideStartCoreAdmission|TestReconcile'`
Expected: PASS(把 Step 2 注释掉的那条恢复)。若 `TestStartCoreStartsWhenScanFindsNoCore` 因 Protection 不是 Protected 而红,查 `newManagerTestEnv` 里 HealthGate 替身是否默认健康(`Up` 在同一 env 下能成功,说明是);不要为了过测试改断言。

- [ ] **Step 5: 整包 + 提交**

Run: `go test ./internal/guardian/`
Expected: PASS。

```bash
git add internal/guardian/reconcile_execute.go internal/guardian/reconcile_execute_test.go
git commit -m "feat(guardian): ③c start_core 执行 —— 槽内现扫准入三态,起 Core 复用 startCoreLocked"
```

---

### Task 3: 每段故障封顶 5 次,socket 应答或用户 Up 归零

**Files:**
- Modify: `internal/guardian/manager.go`(Manager 字段 + `Up` 成功后归零)
- Modify: `internal/guardian/reconcile_loop.go:93-110`(`reconcileOnce` 观测到应答时归零)
- Modify: `internal/guardian/reconcile_execute.go`(`executeStartCore` 计数与封顶)
- Test: `internal/guardian/reconcile_execute_test.go`(追加)

**Interfaces:**
- Consumes:`ReconcileSkipStartCoreExhausted`、`maxReconcileStartCoreAttempts`(Task 1);`executeStartCore`(Task 2)。
- Produces:`Manager.startCoreAttempts atomic.Int32`;`(m *Manager) resetStartCoreAttempts()`。

- [ ] **Step 1: 写失败测试(三条)**

```go
// 每段故障最多 maxReconcileStartCoreAttempts 次:第 6 次不再起,码 start_core_exhausted,
// 且 Start 恰好被调了 5 次。
func TestStartCoreGivesUpAfterTheCap(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.store.SaveDesired(DesiredOn); err != nil {
		t.Fatal(err)
	}
	env.runner.startErr = errors.New("sing-box missing")
	for i := 0; i < maxReconcileStartCoreAttempts; i++ {
		got := env.manager.executeReconcileAction(context.Background(), reconcileDecision{Actions: []reconcileAction{actionStartCore}})
		if got == nil || got.Outcome != reconcileExecutedFailed {
			t.Fatalf("第 %d 次应为 failed,got %+v", i+1, got)
		}
	}
	got := env.manager.executeReconcileAction(context.Background(), reconcileDecision{Actions: []reconcileAction{actionStartCore}})
	if got == nil || got.Outcome != reconcileExecutedSkipped || got.Error != ReconcileSkipStartCoreExhausted {
		t.Fatalf("第 6 次应为 skipped/start_core_exhausted,got %+v", got)
	}
	if env.runner.startCount() != maxReconcileStartCoreAttempts {
		t.Fatalf("Start 被调了 %d 次,want %d", env.runner.startCount(), maxReconcileStartCoreAttempts)
	}
}

// 被扫描拦下的不计次:5 轮 core_process_present 之后仍然可起。
func TestStartCoreScanRefusalsDoNotCountTowardTheCap(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.store.SaveDesired(DesiredOn); err != nil {
		t.Fatal(err)
	}
	env.runner.scanResult = []Process{{PID: 4242}}
	for i := 0; i < maxReconcileStartCoreAttempts+1; i++ {
		env.manager.executeReconcileAction(context.Background(), reconcileDecision{Actions: []reconcileAction{actionStartCore}})
	}
	env.runner.scanResult = nil
	got := env.manager.executeReconcileAction(context.Background(), reconcileDecision{Actions: []reconcileAction{actionStartCore}})
	if got == nil || got.Outcome != reconcileExecutedOK {
		t.Fatalf("被拦下的轮次不该耗掉起的权利,got %+v", got)
	}
}

// 归零的两处各钉一条:观测到 socket 应答;用户 Up 成功。漏一处不会有编译错误。
func TestStartCoreCapResetsWhenCoreIsSeenAnswering(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.store.SaveDesired(DesiredOn); err != nil {
		t.Fatal(err)
	}
	env.runner.startErr = errors.New("sing-box missing")
	for i := 0; i < maxReconcileStartCoreAttempts; i++ {
		env.manager.executeReconcileAction(context.Background(), reconcileDecision{Actions: []reconcileAction{actionStartCore}})
	}
	// 一轮观测看见 socket 应答 ⇒ 段结束。
	env.manager.reconcileOnce(context.Background(), observe.ObservedState{CoreSocket: observe.True})
	env.runner.startErr = nil
	got := env.manager.executeReconcileAction(context.Background(), reconcileDecision{Actions: []reconcileAction{actionStartCore}})
	if got == nil || got.Outcome != reconcileExecutedOK {
		t.Fatalf("段结束后计数应归零,got %+v", got)
	}
}

func TestStartCoreCapResetsAfterUserUp(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.store.SaveDesired(DesiredOn); err != nil {
		t.Fatal(err)
	}
	env.runner.startErr = errors.New("sing-box missing")
	for i := 0; i < maxReconcileStartCoreAttempts; i++ {
		env.manager.executeReconcileAction(context.Background(), reconcileDecision{Actions: []reconcileAction{actionStartCore}})
	}
	env.runner.startErr = nil
	if err := env.manager.Up(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := env.manager.startCoreAttempts.Load(); got != 0 {
		t.Fatalf("用户 Up 成功后计数应归零,got %d", got)
	}
}
```

- [ ] **Step 2: 跑红**

Run: `go test ./internal/guardian/ -run 'TestStartCoreGivesUp|TestStartCoreScanRefusals|TestStartCoreCapResets'`
Expected: 编译失败(`startCoreAttempts` 未定义);先把最后一条注释掉跑,前三条应 FAIL(第 6 次仍 failed / Start 被调 6 次)。

- [ ] **Step 3: 实现**

`manager.go` 的 `Manager` 结构体里,`reconcileWake chan struct{}` 之后加:

```go
	// startCoreAttempts 是 ③c 一段故障里循环起 Core 的次数(封顶
	// maxReconcileStartCoreAttempts)。只在互斥槽内递增;归零在两处:调谐环
	// 观测到 Core socket 应答、用户 Up 成功。原子量:观测在循环 goroutine、
	// 执行在槽内,不为它开第二把锁。
	startCoreAttempts atomic.Int32
```

(`import "sync/atomic"` 若尚未引入则加。)追加方法:

```go
// resetStartCoreAttempts 结束一段故障:Core 又被看见了,或用户亲手起了它。
func (m *Manager) resetStartCoreAttempts() { m.startCoreAttempts.Store(0) }
```

`Up` 里 `if err := m.upLocked(ctx, upOriginUser); err != nil { return err }` 之后、`m.retireCompletedPathRecovery()` 之前加一行 `m.resetStartCoreAttempts()`。

`reconcile_loop.go` 的 `reconcileOnce` 开头(`input := reconcileInput{` 之前)加:

```go
	// ③c:观测到 Core 应答 ⇒ 这段故障结束,起 Core 的次数归零。
	if observed.CoreSocket == observe.True {
		m.resetStartCoreAttempts()
	}
```

`executeStartCore` 在扫描之前加封顶判断,在 `startCoreLocked` 之前递增:

```go
	if m.startCoreAttempts.Load() >= maxReconcileStartCoreAttempts {
		return &ReconcileExecution{Action: string(actionStartCore), Outcome: reconcileExecutedSkipped, Error: ReconcileSkipStartCoreExhausted}, ""
	}
	cores, scanErr, supported := m.scanForStartCore()
	// ...三态判断不变...
	m.startCoreAttempts.Add(1) // 真要起了才计,被扫描拦下的不算
	execCtx, cancelExec := context.WithTimeout(ctx, m.restartTimeout)
```

- [ ] **Step 4: 跑绿**

Run: `go test ./internal/guardian/ -run 'TestStartCore|TestReconcile'`
Expected: PASS(恢复被注释的那条)。

- [ ] **Step 5: 整包 + 提交**

Run: `go test ./internal/guardian/`
Expected: PASS。

```bash
git add internal/guardian/manager.go internal/guardian/reconcile_loop.go internal/guardian/reconcile_execute.go internal/guardian/reconcile_execute_test.go
git commit -m "feat(guardian): ③c start_core 每段故障封顶 5 次,socket 应答或用户 Up 归零"
```

---

### Task 4: 旗舰测试 —— Core 崩溃、首次重启失败、循环把它起回来并释放屏障

**Files:**
- Test: `internal/guardian/reconcile_execute_test.go`(追加)

**Interfaces:**
- Consumes:Task 1–3 全部;`env.manager.handleUnexpectedExit`、`env.manager.runReconcileLoopWithPacing`、`env.events.snapshot()`、`containsEvent`、`env.runner.startCount()`(既有)。

- [ ] **Step 1: 写测试**

```go
// ③c 的旗舰:这条路径此前是无人区 —— Core 意外退出,handleUnexpectedExit 装屏障
// 后那**一次**重启失败,机器停在 Blocked 直到有人敲 bx up。现在循环在下一轮
// 把它起回来,屏障随之释放。变异验证:把白名单改回两项必须转红。
func TestReconcileLoopStartsCoreBackAfterAFailedCrashRestart(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.manager.Up(context.Background()); err != nil {
		t.Fatal(err)
	}
	startsAfterUp := env.runner.startCount()

	// 崩溃 + 那一次自带的重启失败。
	env.runner.startErr = errors.New("sing-box missing")
	env.manager.handleUnexpectedExit(env.runner.currentProcess(), errors.New("Core crashed"))
	if got := env.runner.startCount(); got != startsAfterUp+1 {
		t.Fatalf("handleUnexpectedExit 应自己试过一次重启:start = %d, want %d", got, startsAfterUp+1)
	}
	if !containsEvent(env.events.snapshot(), "barrier.install") {
		t.Fatal("前置不成立:崩溃后没装屏障(fail-closed)")
	}
	env.runner.startErr = nil
	env.events.reset()

	// 循环:socket 不应答直到 Start 被循环调过一次,然后应答并结束。
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	rounds := 0
	observer := func(context.Context) observe.ObservedState {
		mu.Lock()
		defer mu.Unlock()
		rounds++
		socket := observe.False
		if env.runner.startCount() > startsAfterUp+1 {
			socket = observe.True
			cancel()
		}
		if rounds > 50 {
			cancel()
		}
		return observe.ObservedState{CoreSocket: socket, ObservedAt: time.Date(2026, 9, 5, 12, 0, rounds, 0, time.UTC)}
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		env.manager.runReconcileLoopWithPacing(ctx, observer, func(int) time.Duration { return time.Millisecond })
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("循环没有随 ctx 结束")
	}

	if got := env.runner.startCount(); got != startsAfterUp+2 {
		t.Fatalf("循环应恰好起一次 Core:start = %d, want %d", got, startsAfterUp+2)
	}
	if !containsEvent(env.events.snapshot(), "barrier.release") && !containsEvent(env.events.snapshot(), "barrier.remove") {
		t.Fatalf("起回来之后屏障没释放: %v", env.events.snapshot())
	}
	report := env.manager.Status().Reconcile
	if report == nil || report.Executed == nil || report.Executed.Action != string(actionStartCore) || report.Executed.Outcome != reconcileExecutedOK {
		t.Fatalf("Executed = %+v, want start_core/ok", report)
	}
	if got := env.manager.Status().Protection; got != ProtectionProtected {
		t.Fatalf("起回来之后仍是 %q", got)
	}
}
```

- [ ] **Step 2: 跑**

Run: `go test ./internal/guardian/ -run 'TestReconcileLoopStartsCoreBackAfterAFailedCrashRestart' -race`
Expected: PASS。若 `handleUnexpectedExit` 那一步 `start != startsAfterUp+1`,查 `env.store` 的 desired 是否为 on(`Up` 已写);若循环从不起,查 `runReconcileLoopWithPacing` 先睡后观测的第一轮是否被 `Executed` 的 change-only 判断挡住 —— 不该,`reconcileOnce` 每轮都跑 decide。

- [ ] **Step 3: 变异验证**

把 `executableReconcileActions` 里 `actionStartCore: true` 临时删掉,再跑上一条:Expected: FAIL(`循环应恰好起一次 Core:start = 2, want 3` 一类)。**改回来**(用 scratchpad 备份 + cp,不用 git checkout)。

- [ ] **Step 4: 提交**

```bash
git add internal/guardian/reconcile_execute_test.go
git commit -m "test(guardian): ③c 旗舰 —— 崩溃后首次重启失败,循环起回 Core 并释放屏障"
```

---

### Task 5: `bx status` 渲染三个新码;exhausted 不许说「让路」

**Files:**
- Modify: `internal/cli/cli.go:4605-4626`(`reconcileRoundExecution`)
- Test: `internal/cli/reconcile_execution_render_test.go`(追加)

**Interfaces:**
- Consumes:`guardian.ReconcileSkipStartCoreExhausted`、`guardian.ReconcileSkipCoreProcessPresent`、`guardian.ReconcileSkipCoreScanFailed`(Task 1)。

- [ ] **Step 1: 写失败测试**

```go
// ③c:start_core 的三个码各有一句人话。exhausted 是「放弃、等人」,不是让路 ——
// 让路是暂时的,放弃是要人来的,把它渲染成让路会让用户以为过会儿就好。
func TestReconcileLineRendersStartCoreCodesAsActionableSentences(t *testing.T) {
	now := time.Now()
	round := guardian.ReconcileReport{At: now.Add(-time.Minute)}

	round.Executed = &guardian.ReconcileExecution{Action: "start_core", Outcome: "skipped", Error: guardian.ReconcileSkipStartCoreExhausted}
	line := reconcileRoundSummary(round, now)
	if strings.Contains(line, "让路") {
		t.Fatalf("放弃被渲染成了让路: %q", line)
	}
	if !strings.Contains(line, "已放弃") || !strings.Contains(line, "sudo bx up") {
		t.Fatalf("exhausted 要说「已放弃」并指路 sudo bx up: %q", line)
	}

	round.Executed = &guardian.ReconcileExecution{Action: "start_core", Outcome: "skipped", Error: guardian.ReconcileSkipCoreProcessPresent}
	line = reconcileRoundSummary(round, now)
	if !strings.Contains(line, "Core 进程") || !strings.Contains(line, "socket") {
		t.Fatalf("core_process_present 要说出「有 Core 进程在跑但 socket 不应答」: %q", line)
	}

	round.Executed = &guardian.ReconcileExecution{Action: "start_core", Outcome: "skipped", Error: guardian.ReconcileSkipCoreScanFailed}
	line = reconcileRoundSummary(round, now)
	if !strings.Contains(line, "问不出") {
		t.Fatalf("core_scan_failed 要说「问不出有没有 Core 在跑」: %q", line)
	}
}
```

- [ ] **Step 2: 跑红**

Run: `go test ./internal/cli/ -run 'TestReconcileLineRendersStartCoreCodesAsActionableSentences'`
Expected: FAIL(`放弃被渲染成了让路`)。

- [ ] **Step 3: 实现**

`reconcileRoundExecution` 的 `case "skipped":` 改为:

```go
	case "skipped":
		// ③c 的三个码不是让路:放弃要人来,另外两个是「为什么没起」的答案。
		switch executed.Error {
		case guardian.ReconcileSkipStartCoreExhausted:
			return segment + "(已放弃:连续 5 次起不来,等你 sudo bx up;原因见 /var/log/bx-guard.err.log)"
		case guardian.ReconcileSkipCoreProcessPresent:
			return segment + "(有 Core 进程在跑但控制 socket 不应答,没起第二个)"
		case guardian.ReconcileSkipCoreScanFailed:
			return segment + "(问不出有没有 Core 在跑,没起)"
		}
		return segment + "(让路: " + executed.Error + ")"
```

- [ ] **Step 4: 跑绿 + 提交**

Run: `go test ./internal/cli/ -run 'TestReconcileLine'`
Expected: PASS。

```bash
git add internal/cli/cli.go internal/cli/reconcile_execution_render_test.go
git commit -m "feat(cli): bx status 渲染 ③c 的三个 start_core 码,exhausted 说「已放弃」不说让路"
```

---

### Task 6: CLAUDE.md 一节 + 全量闸门

**Files:**
- Modify: `CLAUDE.md`(在「## 调谐环第一批执行权(阶段③b……)」之前插入一节)
- Modify: `internal/guardian/reconcile_execute.go` 文件头注释(若 Task 1 未改全)

- [ ] **Step 1: 写 CLAUDE.md 一节**

在 `## 调谐环第一批执行权(阶段③b,2026-08-29,真机未验)` 之前插入:

```markdown
## 调谐环执行 start_core(阶段③c,2026-09-05,真机未验)

**修的是一条无人区路径**:Core 意外退出时 `handleUnexpectedExit` 装屏障后重启**一次**,
失败(`core_restart_failed`)之后没有任何东西再试,机器停在 Blocked 直到有人敲
`sudo bx up`。现在白名单三项(`restore_dns`/`clear_orphan_barrier`/`start_core`,
`TestExecutableWhitelistIsExactlyOffCleanupPlusStartCore` 钉死)。**准入是槽内现扫
`ScanRunning`,不是 socket**(`decideStartCoreAdmission` 三态:测成 0 个才起;≥1 个
→ `core_process_present`,那是卡住但活着的 Core,起第二个正是 af81632 双 Core 的入口,
本期只显形;没测成 → `core_scan_failed`,「问不出来」不是「没有」)。起 Core 复用
`startCoreLocked`(带屏障 handoff、等健康、成功释放屏障),与 `bx up` 同一条路,
`runner.Start` 既有的 fail-closed 准入一道不拆。**每段故障封顶 5 次**
(`maxReconcileStartCoreAttempts`,段 = socket 首次不应答 → 再次应答或用户 `Up`),
被扫描拦下的不计次;过了发布 `start_core_exhausted`,`bx status` 渲染成「已放弃,
等你 sudo bx up」——**不许渲染成让路**。与 ③b「清理永不放弃」刻意不同:清理幂等,
起进程不是。槽内前置条件从写死的 `desired==off` 改成按动作(`requiredDesired`)。
`stop_core`、重启卡住的 Core、装屏障、解 Uncertain 锁存四样仍不做(spec「不做」)。
旗舰测试 `TestReconcileLoopStartsCoreBackAfterAFailedCrashRestart`(白名单改回两项即红)。
**真机验收**(所有者在场):把 data_dir 里的 sing-box 暂时改名,`sudo kill -9 <Core PID>`,
看日志 `core_unexpected_exit` → `core_restart_failed` → 循环 `start_core … execute_failed`
五次 → `start_core_exhausted`;改回名字、`sudo bx up` 归零回绿。
spec `docs/superpowers/specs/2026-09-05-stage3c-start-core-design.md`。
```

- [ ] **Step 2: 守卫**

Run: `go test ./internal/cli/ -run 'TestDocumentedFilePathsExist|TestEveryTestNameMentionedInProseExists'`
Expected: PASS。

- [ ] **Step 3: 全量闸门**

Run: `bash scripts/verify.sh`
Expected: `✓ verify passed`(12 步全绿,含 race 与交叉编译)。红了就修,不许跳过。

- [ ] **Step 4: 提交**

```bash
git add CLAUDE.md internal/guardian/reconcile_execute.go
git commit -m "docs: CLAUDE.md 记 ③c —— 调谐环执行 start_core 的准入、封顶与不做"
```
