package guardian

import (
	"context"
	"log"
	"time"
)

// 阶段③b:调谐环的第一批执行权。
// spec:docs/superpowers/specs/2026-08-29-stage3b-cleanup-actions-design.md。
//
// 授权面**只有 desired=off 的两个清理动作**——用户已明确说了「关」,替它清
// 残留不违背任何意图,且各自对应一次真实事故的形状(孤儿屏障打死整机连通、
// DNS 残留打死整机解析)。
//
// 观察态的两个,理由写死在 spec 里,别在这里重新提议:
//   - stop_core:desired=off + socket 应答最常见的来源是 `sudo bx run`
//     (文档化的调试路径),每 30 秒杀一次调试进程的调谐器是敌意软件;
//   - start_core:CoreSocket==False 是「socket 没应答」不是「没有 Core 在跑」,
//     按它起新 Core 正是 af81632 双 Core 的入口(decide 的注释同一段话)。
var executableReconcileActions = map[reconcileAction]bool{
	actionRestoreDNS:         true,
	actionClearOrphanBarrier: true,
}

const (
	reconcileExecutedOK      = "ok"
	reconcileExecutedFailed  = "failed"
	reconcileExecutedSkipped = "skipped"
	// reconcileSkipPreconditions:拿到槽之后复核发现意图/栅栏变了 —— 决策与
	// 执行之间用户可能刚好 up,按陈旧决策拆用户刚要起来的东西是本文件最不可
	// 犯的错。这不是故障,是让路。
	reconcileSkipPreconditions = "preconditions_changed"
	// reconcileExecuteTimeout 给一次执行封顶。执行期间持着 mutation 槽,
	// 用户的 up/down 最坏等这么久 —— 与一次真实 down 的量级相当。
	reconcileExecuteTimeout = 20 * time.Second
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
	result := m.executeUnderMutationSlot(ctx, action)
	log.Printf("guardian_reconcile_executed action=%s outcome=%s err=%s",
		result.Action, result.Outcome, formatExecutionError(result.Error))
	return result
}

func (m *Manager) executeUnderMutationSlot(ctx context.Context, action reconcileAction) *ReconcileExecution {
	// try-acquire,**不排队**:channel 的唤醒是 FIFO 的,硬等会把用户那次
	// `bx up` 挤过预算(与 readMutationFences 同一条纪律、同一个时限)。
	acquireCtx, cancel := context.WithTimeout(ctx, reconcileMutationWait)
	defer cancel()
	if err := m.acquireMutation(acquireCtx); err != nil {
		return &ReconcileExecution{Action: string(action), Outcome: reconcileExecutedSkipped, Error: heldMutationBusy}
	}
	defer m.releaseMutation()

	// 槽内复核:决策用的意图是槽外读的,拿到槽之前用户可能刚好 up/武装了
	// 挂起/锁存升起。授权的两个动作都只对 desired=off 成立,任何一项对不上
	// 就整轮放弃 —— 下一轮按新事实从头判。
	intent, err := m.loadIntentSnapshot(time.Now())
	if err != nil || intent.Desired != DesiredOff || intent.HoldArmed ||
		m.recoveryBlocked || m.current.Uncertain {
		return &ReconcileExecution{Action: string(action), Outcome: reconcileExecutedSkipped, Error: reconcileSkipPreconditions}
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
		return &ReconcileExecution{Action: string(action), Outcome: reconcileExecutedFailed, Error: runErr.Error()}
	}
	return &ReconcileExecution{Action: string(action), Outcome: reconcileExecutedOK}
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
