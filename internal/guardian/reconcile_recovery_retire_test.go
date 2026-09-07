package guardian

import (
	"context"
	"testing"

	"github.com/getbx/bx/internal/observe"
)

// 真机 2026-09-07:一小时的 50 秒一拍睡眠/暗唤醒里,recovery-10 在 verify 连败
// 20 次后放弃;机器真正醒来后隧道、捕获、DNS 全部自愈,而那份 failed 快照没有任何
// 东西会清 —— `bx status` 与菜单一直 Blocked、图标裂开,同一屏 observed 说屏障不在、
// 隧道健康。09-04 的 retireCompletedPathRecovery 只挂在用户的 up/down 上,用户
// 什么都没做时它永远不跑。调谐环每轮都拿着观测,它正是该纠正这份陈旧信念的人。
func TestReconcileOnceRetiresAFailedPathRecoveryTheKernelContradicts(t *testing.T) {
	env := newManagerTestEnv(t)
	env.manager.setStatus(Status{SchemaVersion: 1, Desired: DesiredOn, Phase: PhaseCommitted, Protection: ProtectionProtected})
	env.manager.pathRecoveryCurrent = RecoverySnapshot{
		ID: "recovery-10", State: "failed", Stage: "verify", Reason: "underlay_changed", ErrorCode: "verification_failed", Attempt: 20,
	}

	env.manager.reconcileOnce(context.Background(), observedProtectedState())

	if got := env.manager.CurrentPathRecovery(); got.State != "idle" {
		t.Fatalf("观测已证明机器受保护,失败的恢复快照该退场,got %+v", got)
	}
}

func observedProtectedState() observe.ObservedState {
	return observe.ObservedState{
		CaptureOK:      observe.True,
		BarrierPresent: observe.False,
		DNSManaged:     observe.True,
		CoreSocket:     observe.True,
		TunnelHealthy:  observe.True,
	}
}

// 反向:观测没有证明受保护、或恢复还没结束、或 Manager 自己就说 Blocked(屏障
// 真的在手里)时,一个字都不许动 —— 清掉一份仍然成立的失败就是把 Blocked 说成
// Protected,方向正好是这个功能最忌讳的那种错。
func TestReconcileOnceKeepsAFailedPathRecoveryTheKernelDoesNotContradict(t *testing.T) {
	failed := RecoverySnapshot{ID: "recovery-10", State: "failed", Stage: "verify", Reason: "underlay_changed", ErrorCode: "verification_failed"}
	tests := []struct {
		name       string
		protection string
		snapshot   RecoverySnapshot
		mutate     func(*observe.ObservedState)
	}{
		{name: "tunnel unhealthy", protection: ProtectionProtected, snapshot: failed, mutate: func(o *observe.ObservedState) { o.TunnelHealthy = observe.False }},
		{name: "capture unknown", protection: ProtectionProtected, snapshot: failed, mutate: func(o *observe.ObservedState) { o.CaptureOK = observe.Unknown }},
		{name: "barrier present", protection: ProtectionProtected, snapshot: failed, mutate: func(o *observe.ObservedState) { o.BarrierPresent = observe.True }},
		{name: "dns not managed", protection: ProtectionProtected, snapshot: failed, mutate: func(o *observe.ObservedState) { o.DNSManaged = observe.False }},
		{name: "core socket unknown", protection: ProtectionProtected, snapshot: failed, mutate: func(o *observe.ObservedState) { o.CoreSocket = observe.Unknown }},
		{name: "manager itself says blocked", protection: ProtectionBlocked, snapshot: failed, mutate: func(*observe.ObservedState) {}},
		{name: "recovery still running", protection: ProtectionProtected, snapshot: RecoverySnapshot{ID: "recovery-11", State: "running", Stage: "verify", Reason: "underlay_changed"}, mutate: func(*observe.ObservedState) {}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newManagerTestEnv(t)
			env.manager.setStatus(Status{SchemaVersion: 1, Desired: DesiredOn, Phase: PhaseCommitted, Protection: tt.protection})
			env.manager.pathRecoveryCurrent = tt.snapshot
			observed := observedProtectedState()
			tt.mutate(&observed)

			env.manager.reconcileOnce(context.Background(), observed)

			if got := env.manager.CurrentPathRecovery(); got != tt.snapshot {
				t.Fatalf("这份快照不该被动\nwant %+v\ngot  %+v", tt.snapshot, got)
			}
		})
	}
}
