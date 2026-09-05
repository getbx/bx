package guardian

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getbx/bx/internal/observe"
)

// 阶段③b 的执行器测试。spec:2026-08-29-stage3b-cleanup-actions-design.md。
// 授权面只有 desired=off 的两个清理动作;这里的每条测试对应 spec 里
// 「执行的五条纪律」之一,变异验证过。

// 一轮至多执行一个动作,且是提议序里第一个**被授权**的:观测同时给出屏障
// 残留与 DNS 残留时,本轮只清屏障(提议序 stop_core→barrier→dns,stop_core
// 未授权被跳过),restore_dns 留给下一轮按新观测再说 —— 连发第二个就是按
// 执行前的陈旧事实行动。
func TestReconcileLoopExecutesTheFirstAuthorizedCleanupAndOnlyOne(t *testing.T) {
	env := newManagerTestEnv(t)
	logs := captureGuardianLog(t)
	// 替身默认不记 DNS 事件 —— 不打开它,下面「restore_dns 不该被执行」的
	// 断言就是空转的(变异 3 实测:同轮多执行一个动作照样全绿)。
	env.dns.mu.Lock()
	env.dns.record = true
	env.dns.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	rounds := 0
	observer := func(context.Context) observe.ObservedState {
		mu.Lock()
		defer mu.Unlock()
		rounds++
		if rounds > 1 {
			cancel()
		}
		return observe.ObservedState{
			BarrierPresent: observe.True,
			DNSManaged:     observe.True,
			CoreSocket:     observe.False,
			ObservedAt:     time.Date(2026, 8, 29, 12, 0, rounds, 0, time.UTC),
		}
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

	if got := env.orphanBarrierClears(); got != 1 {
		t.Fatalf("clear_orphan_barrier 应执行恰好一次,got %d", got)
	}
	if events := strings.Join(env.events.snapshot(), ","); strings.Contains(events, "dns.restore") {
		t.Fatalf("一轮至多一个动作,restore_dns 不该在同一轮被执行: %s", events)
	}
	report := env.manager.Status().Reconcile
	if report == nil || report.Executed == nil {
		t.Fatal("执行结果必须进报告")
	}
	if report.Executed.Action != string(actionClearOrphanBarrier) || report.Executed.Outcome != reconcileExecutedOK {
		t.Fatalf("Executed = %+v", report.Executed)
	}
	if lines := logLinesContaining(logs, "guardian_reconcile_executed"); len(lines) == 0 {
		t.Fatal("每次执行必须留一行 guardian_reconcile_executed")
	}
}

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

// 白名单外的动作**永远**不执行 —— 穷举全部已命名动作,授权名单之外的每一个
// 单独喂进执行器都必须返回 nil 且零副作用(与 ③a「不许有装屏障动作」同款
// 穷举,防「加一个新动作顺手执行」)。
func TestReconcileExecutionRefusesEveryUnauthorizedAction(t *testing.T) {
	all := []reconcileAction{actionRestoreDNS, actionClearOrphanBarrier, actionStartCore, actionStopCore}
	for _, action := range all {
		if executableReconcileActions[action] {
			continue
		}
		env := newManagerTestEnv(t)
		before := env.mutationCallCounts()
		got := env.manager.executeReconcileAction(context.Background(), reconcileDecision{
			Actions: []reconcileAction{action},
		})
		if got != nil {
			t.Fatalf("未授权动作 %s 被执行器接了: %+v", action, got)
		}
		if after := env.mutationCallCounts(); after != before {
			t.Fatalf("未授权动作 %s 产生了副作用\nbefore=%+v\nafter =%+v", action, before, after)
		}
	}
}

// 互斥槽忙(用户的 up/down 正在跑)⇒ 整轮跳过并如实记 skipped,绝不排队 ——
// 调谐器排进 FIFO 会把用户那次 up 挤过预算。
func TestReconcileExecutionSkipsWhenMutationSlotIsBusy(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.manager.acquireMutation(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer env.manager.releaseMutation()

	got := env.manager.executeReconcileAction(context.Background(), reconcileDecision{
		Actions: []reconcileAction{actionClearOrphanBarrier},
	})
	if got == nil || got.Outcome != reconcileExecutedSkipped || got.Error != heldMutationBusy {
		t.Fatalf("Executed = %+v, want skipped/mutation_busy", got)
	}
	if got := env.orphanBarrierClears(); got != 0 {
		t.Fatal("槽忙时不许执行")
	}
}

// 槽内复核:决策与拿到槽之间用户可能刚好 up 了。意图不再是「关」就整轮放弃,
// 绝不按陈旧决策拆一个用户刚要起来的东西。
func TestReconcileExecutionRechecksIntentUnderTheSlot(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.store.SaveDesired(DesiredOn); err != nil {
		t.Fatal(err)
	}
	got := env.manager.executeReconcileAction(context.Background(), reconcileDecision{
		Actions: []reconcileAction{actionClearOrphanBarrier},
	})
	if got == nil || got.Outcome != reconcileExecutedSkipped || got.Error != reconcileSkipPreconditions {
		t.Fatalf("Executed = %+v, want skipped/preconditions_changed", got)
	}
	if got := env.orphanBarrierClears(); got != 0 {
		t.Fatal("意图已变仍执行了清理")
	}
}

// 失败如实进报告,不放弃:残留还在 ⇒ 下一轮照旧提议 ⇒ 退避天然限频。
// 「连败 N 次就停」是手写补偿时代的形状 —— 停了之后残留永久无人管。
func TestReconcileExecutionReportsFailureAndTheLoopRetries(t *testing.T) {
	env := newManagerTestEnv(t)
	logs := captureGuardianLog(t)
	env.clearOrphanBarrierErr = errors.New("route: operation not permitted")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	rounds := 0
	observer := func(context.Context) observe.ObservedState {
		mu.Lock()
		defer mu.Unlock()
		rounds++
		if rounds > 2 {
			cancel()
		}
		return observe.ObservedState{
			BarrierPresent: observe.True,
			CoreSocket:     observe.False,
			ObservedAt:     time.Date(2026, 8, 29, 13, 0, rounds, 0, time.UTC),
		}
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

	if got := env.orphanBarrierClears(); got < 2 {
		t.Fatalf("失败后下一轮应照旧重试,got %d 次尝试", got)
	}
	report := env.manager.Status().Reconcile
	if report == nil || report.Executed == nil || report.Executed.Outcome != reconcileExecutedFailed {
		t.Fatalf("失败必须如实进报告: %+v", report)
	}
	// **发布面只带码**:Status 走 0666 socket,原始错误串(命令行/命令输出)
	// 只进 Guardian 日志 ——「响应体只带失败码」的记档不变量,这里不开例外。
	if report.Executed.Error != reconcileExecuteFailedCode {
		t.Fatalf("发布面上只许是失败码,got %+v", report.Executed)
	}
	if strings.Contains(report.Executed.Error, "not permitted") {
		t.Fatalf("原始错误串漏进了发布面: %+v", report.Executed)
	}
	// 完整原因必须在日志里 —— 只删不移就是把线索弄丢。
	if lines := logLinesContaining(logs, "not permitted"); len(lines) == 0 {
		t.Fatal("完整失败原因必须进 Guardian 日志")
	}
}

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
