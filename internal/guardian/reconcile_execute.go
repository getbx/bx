package guardian

import (
	"context"
	"fmt"
	"log"
	"time"
)

// 阶段③b:调谐环的第一批执行权;③c 加了 start_core。
// spec:docs/superpowers/specs/2026-08-29-stage3b-cleanup-actions-design.md、
//
//	docs/superpowers/specs/2026-09-05-stage3c-start-core-design.md。
//
// 授权面是 desired=off 的两个清理动作 + desired=on 的 start_core:
//   - 清理:用户已明确说了「关」,替它清残留不违背任何意图,且各自对应一次
//     真实事故的形状(孤儿屏障打死整机连通、DNS 残留打死整机解析);
//   - start_core(③c):Core 意外退出后那一次自带的重启失败之后,此前没有任何
//     东西再试。准入是槽内现扫 ScanRunning(测成 0 个才起),起 Core 走
//     startCoreLocked(与 bx up 同一条原语),每段故障最多
//     maxReconcileStartCoreAttempts 次。
//
// 观察态的一个,理由写死在 spec 里,别在这里重新提议:
//   - stop_core:desired=off + socket 应答最常见的来源是 `sudo bx run`
//     (文档化的调试路径),每 30 秒杀一次调试进程的调谐器是敌意软件。
var executableReconcileActions = map[reconcileAction]bool{
	actionRestoreDNS:         true,
	actionClearOrphanBarrier: true,
	actionStartCore:          true,
}

const (
	reconcileExecutedOK      = "ok"
	reconcileExecutedFailed  = "failed"
	reconcileExecutedSkipped = "skipped"
	// reconcileSkipPreconditions:拿到槽之后复核发现意图变了 —— 决策与
	// 执行之间用户可能刚好 up,按陈旧决策拆用户刚要起来的东西是本文件最不可
	// 犯的错。这不是故障,是让路。栅栏升起与意图读不出各用自己的名字
	// (heldBy 的产出),不折进这一个 —— intent_unreadable 是真故障,
	// 折进「让路」就把它渲染成永远的良性。
	reconcileSkipPreconditions = "preconditions_changed"
	// reconcileExecuteFailedCode:发布面只带失败码 —— Executed 进 Status、
	// Status 走 0666 的本机 socket,原始错误串(命令行 + 命令输出,可能含
	// 路径)只进 Guardian 日志。「响应体只带失败码」是记档不变量,这里不开
	// 第三个例外。
	reconcileExecuteFailedCode = "execute_failed"
	// reconcileExecuteTimeout 给一次执行封顶。执行期间持着 mutation 槽,
	// 用户的 up/down 最坏等这么久 —— 与一次真实 down 的量级相当。
	reconcileExecuteTimeout = 20 * time.Second
	// ③c start_core 的三个稳定码。导出:internal/cli 渲染用同一份名字,
	// 不许两边各抄一份字符串(与 internal/udpsource 那条纪律同源)。
	//   - core_process_present:扫到 ≥1 个 Core 进程,socket 却不应答 —— 卡住但
	//     活着,起第二个正是 af81632 双 Core 的入口,本期只显形不处置;
	//   - core_scan_failed:没测成。「问不出来」不是「没有」;
	//   - start_core_exhausted:本段故障已试满 maxReconcileStartCoreAttempts 次。
	ReconcileSkipCoreProcessPresent = "core_process_present"
	ReconcileSkipCoreScanFailed     = "core_scan_failed"
	ReconcileSkipStartCoreExhausted = "start_core_exhausted"
	// maxReconcileStartCoreAttempts 是每段故障(socket 首次不应答 → 再次应答)
	// 里起 Core 的上限。起进程不幂等:一份坏配置被每 30 秒起一次杀一次是敌意
	// 软件。5 没有真机依据,取保守值。
	maxReconcileStartCoreAttempts = 5
)

// firstExecutableAction 返回提议序里第一个被授权的动作。
// 一轮至多执行一个:第二个动作是按**执行前**的观测提议的,执行完第一个之后
// 那份观测已经陈旧 —— 下一轮重新观测再说。
func firstExecutableAction(actions []reconcileAction) (reconcileAction, bool) {
	for _, action := range actions {
		if executableReconcileActions[action] {
			return action, true
		}
	}
	return actionNone, false
}

// executeReconcileAction 执行本轮提议里第一个被授权的动作,返回结果供报告与
// 日志;没有可执行的就返回 nil(健康机器的常态)。
//
// 五条纪律(spec)在此落点:互斥槽 try-acquire 不排队、槽内复核意图、一轮
// 至多一个、失败如实上报不放弃(残留还在 ⇒ 下一轮照旧提议 ⇒ 退避天然限频)、
// 动作全部复用既有原语。
func (m *Manager) executeReconcileAction(ctx context.Context, decision reconcileDecision) *ReconcileExecution {
	if decision.Held != "" {
		return nil
	}
	action, ok := firstExecutableAction(decision.Actions)
	if !ok {
		return nil
	}
	result, detail := m.executeUnderMutationSlot(ctx, action)
	// 完整原因(可能含命令行与命令输出)只进 Guardian 日志;发布面上的
	// result.Error 是稳定的码。
	log.Printf("guardian_reconcile_executed action=%s outcome=%s code=%s detail=%s",
		result.Action, result.Outcome, formatExecutionError(result.Error), formatExecutionError(detail))
	return result
}

// executeUnderMutationSlot 在互斥槽内复核并执行。第二个返回值是**只进日志**
// 的完整失败原因(发布面上 result.Error 只有码)。
func (m *Manager) executeUnderMutationSlot(ctx context.Context, action reconcileAction) (*ReconcileExecution, string) {
	// try-acquire,**不排队**:channel 的唤醒是 FIFO 的,硬等会把用户那次
	// `bx up` 挤过预算(与 readMutationFences 同一条纪律、同一个时限)。
	acquireCtx, cancel := context.WithTimeout(ctx, reconcileMutationWait)
	defer cancel()
	if err := m.acquireMutation(acquireCtx); err != nil {
		return &ReconcileExecution{Action: string(action), Outcome: reconcileExecutedSkipped, Error: heldMutationBusy}, ""
	}
	defer m.releaseMutation()

	// 槽内复核经 **heldBy 本尊**,不手抄栅栏清单:决策与拿到槽之间任何一道
	// 栅栏都可能升起(路径恢复不走 mutation 槽,它有自己的锁 —— 手抄清单
	// 漏掉它,清理就会与一次在飞的路由手术并发写路由表;将来加第六道栅栏,
	// 手抄清单也不会跟着长)。skipped 的 Error 就是栅栏名,
	// intent_unreadable 因此保住自己的名字 —— 它是真故障,不折进「让路」。
	input := reconcileInput{
		PathRecoveryBusy:   m.pathRecoveryBusy(),
		RecoveryBlocked:    m.recoveryBlocked,
		OwnershipUncertain: m.current.Uncertain,
	}
	intent, err := m.loadIntentSnapshot(time.Now())
	if err != nil {
		input.IntentUnreadable = true
	} else {
		input.Desired = intent.Desired
		input.MaintenanceHold = intent.HoldArmed
	}
	if held := heldBy(input); held != "" {
		return &ReconcileExecution{Action: string(action), Outcome: reconcileExecutedSkipped, Error: held}, ""
	}
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
	case actionClearOrphanBarrier:
		// 逃生口同款原语:孤儿按定义没有所有权记账,ownership-free 的清理
		// 才够得着它(linux 版 2026-08-29 已同批供货)。
		runErr = m.clearOrphanBarrier(execCtx)
	case actionRestoreDNS:
		// 经 Manager 的 restoreDNS 而不是裸调 install:dnsStatus 缓存与状态
		// 发布要跟着动。
		runErr = m.restoreDNS(execCtx)
	}
	if runErr != nil {
		return &ReconcileExecution{Action: string(action), Outcome: reconcileExecutedFailed, Error: reconcileExecuteFailedCode}, runErr.Error()
	}
	return &ReconcileExecution{Action: string(action), Outcome: reconcileExecutedOK}, ""
}

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

// executeStartCore 在槽内(调用方已持 mutation 槽、已复核栅栏与意图)起 Core。
// 顺序:现扫 → 三态 → startCoreLocked。超时取 restartTimeout 而不是
// reconcileExecuteTimeout:起 Core 要等健康,与 handleUnexpectedExit 那次重启
// 同一个预算。
func (m *Manager) executeStartCore(ctx context.Context) (*ReconcileExecution, string) {
	// 封顶先于扫描:试满了就不再花 900 次 syscall 去问。
	if m.startCoreAttempts.Load() >= maxReconcileStartCoreAttempts {
		return &ReconcileExecution{Action: string(actionStartCore), Outcome: reconcileExecutedSkipped, Error: ReconcileSkipStartCoreExhausted}, ""
	}
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
	// 真要起了才计:被扫描拦下的不算,否则一个卡住的 Core 会在五轮之后把
	// 「起」的权利也耗光,而那正是最该留着的时候。
	m.startCoreAttempts.Add(1)
	execCtx, cancelExec := context.WithTimeout(ctx, m.restartTimeout)
	defer cancelExec()
	if _, err := m.startCoreLocked(execCtx); err != nil {
		return &ReconcileExecution{Action: string(actionStartCore), Outcome: reconcileExecutedFailed, Error: reconcileExecuteFailedCode}, err.Error()
	}
	return &ReconcileExecution{Action: string(actionStartCore), Outcome: reconcileExecutedOK}, ""
}

func formatExecutionError(err string) string {
	if err == "" {
		return "none"
	}
	return err
}

// sameReconcileExecution 判两次执行结果是否相同(变化比较用;nil = 没执行)。
func sameReconcileExecution(a, b *ReconcileExecution) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
