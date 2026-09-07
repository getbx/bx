package guardian

import (
	"context"
	"errors"
	"testing"

	"github.com/getbx/bx/internal/stats"
	"github.com/getbx/bx/internal/supervisor"
)

// 真机 2026-09-07:失败的恢复快照让 `bx status` 与菜单 Blocked 一个多小时,
// 而机器能上网。调谐环按内核观测退场要等一个退避周期(最长 10 分钟);
// 用户的感受是「能上网,菜单裂开」—— 那 10 分钟对他就是 bx 坏了。
// Guardian 每次答状态时手里就有 Core 的运行时事实(菜单每 2 秒问一次),
// 而那几项正是 verify 要看的:答状态那一刻就该按事实判,不该等循环。
func TestObservableStatusStopsCallingAFailedRecoveryBlockedOnceTheCoreVerifiesItself(t *testing.T) {
	controller := &fakeController{
		status:          Status{SchemaVersion: 1, Desired: DesiredOn, Phase: PhaseCommitted, Protection: ProtectionProtected, DNSState: DNSManaged},
		recoveryCurrent: failedVerifyRecovery(),
	}
	options := LocalAPIOptions{CoreRuntime: func(context.Context) (CoreRuntime, error) { return coreVerified(), nil }}

	got := observableStatus(controller, controller, options)

	if got.Protection != ProtectionProtected {
		t.Fatalf("Core 已满足 verify 的每一项,失败的恢复是历史,protection = %q, want protected", got.Protection)
	}
	if got.Recovery.State != "idle" {
		t.Fatalf("被事实否定的失败快照不该再对外发布,recovery = %+v", got.Recovery)
	}
}

func failedVerifyRecovery() RecoverySnapshot {
	return RecoverySnapshot{ID: "recovery-10", State: "failed", Stage: "verify", Reason: "underlay_changed", ErrorCode: "verification_failed", Attempt: 20}
}

func coreVerified() CoreRuntime {
	return CoreRuntime{Reachable: true, TunnelHealthy: true, RoutesInstalled: true, DNSListening: true, UDPRequired: true, UDPReady: true}
}

// 反向:Core 答不上来 / 任一项不满足 / Manager 自己就不是 Protected(屏障真在
// 手里、或需要修理)时,失败照旧发布、Blocked 照旧 —— 一次没拿到答案的探测
// 不许被读成「好了」。
func TestObservableStatusKeepsAFailedRecoveryBlockedWhileTheCoreDoesNotVerify(t *testing.T) {
	tests := []struct {
		name           string
		protection     string
		core           func() (CoreRuntime, error)
		wantProtection string
	}{
		{"core unreachable", ProtectionProtected, func() (CoreRuntime, error) { return CoreRuntime{}, errors.New("dial") }, ProtectionBlocked},
		{"tunnel unhealthy", ProtectionProtected, func() (CoreRuntime, error) { c := coreVerified(); c.TunnelHealthy = false; return c, nil }, ProtectionBlocked},
		{"routes not installed", ProtectionProtected, func() (CoreRuntime, error) { c := coreVerified(); c.RoutesInstalled = false; return c, nil }, ProtectionBlocked},
		{"dns not listening", ProtectionProtected, func() (CoreRuntime, error) { c := coreVerified(); c.DNSListening = false; return c, nil }, ProtectionBlocked},
		{"udp required but not ready", ProtectionProtected, func() (CoreRuntime, error) { c := coreVerified(); c.UDPReady = false; return c, nil }, ProtectionBlocked},
		{"no core runtime provider", ProtectionProtected, nil, ProtectionBlocked},
		{"manager holds the barrier", ProtectionBlocked, func() (CoreRuntime, error) { return coreVerified(), nil }, ProtectionBlocked},
		{"manager needs attention", ProtectionNeedsAttention, func() (CoreRuntime, error) { return coreVerified(), nil }, ProtectionNeedsAttention},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			controller := &fakeController{
				status:          Status{SchemaVersion: 1, Desired: DesiredOn, Phase: PhaseCommitted, Protection: tt.protection, DNSState: DNSManaged},
				recoveryCurrent: failedVerifyRecovery(),
			}
			options := LocalAPIOptions{}
			if tt.core != nil {
				core := tt.core
				options.CoreRuntime = func(context.Context) (CoreRuntime, error) { return core() }
			}

			got := observableStatus(controller, controller, options)

			if got.Protection != tt.wantProtection {
				t.Fatalf("protection = %q, want %q", got.Protection, tt.wantProtection)
			}
			if got.Recovery.State != "failed" {
				t.Fatalf("失败快照不该被藏起来,recovery = %+v", got.Recovery)
			}
		})
	}
}

// UDP 没配专用传输时 UDPReady 恒 false,不许因此永远判「没好」。
func TestObservableStatusIgnoresUDPReadinessWhenNoUDPTransportIsRequired(t *testing.T) {
	controller := &fakeController{
		status:          Status{SchemaVersion: 1, Desired: DesiredOn, Phase: PhaseCommitted, Protection: ProtectionProtected, DNSState: DNSManaged},
		recoveryCurrent: failedVerifyRecovery(),
	}
	options := LocalAPIOptions{CoreRuntime: func(context.Context) (CoreRuntime, error) {
		c := coreVerified()
		c.UDPRequired, c.UDPReady = false, false
		return c, nil
	}}
	if got := observableStatus(controller, controller, options); got.Protection != ProtectionProtected {
		t.Fatalf("protection = %q, want protected", got.Protection)
	}
}

// 不只是投影:Manager 手里那份记忆也要退场,否则 /v1/recoveries 与 /v1/status
// 对同一个问题给两个答案。
func TestObservableStatusRetiresTheSupersededRecoveryFromManagerMemory(t *testing.T) {
	env := newManagerTestEnv(t)
	env.manager.setStatus(Status{SchemaVersion: 1, Desired: DesiredOn, Phase: PhaseCommitted, Protection: ProtectionProtected})
	env.manager.pathRecoveryCurrent = failedVerifyRecovery()
	options := LocalAPIOptions{CoreRuntime: func(context.Context) (CoreRuntime, error) { return coreVerified(), nil }}

	observableStatus(env.manager, env.manager, options)

	if got := env.manager.CurrentPathRecovery(); got.State != "idle" {
		t.Fatalf("Manager 记忆里的失败快照该退场,got %+v", got)
	}
}

// verify 看的那几项要真的从 Core 的 RuntimeState 搬进 CoreRuntime;
// RuntimeState 问不出来时它们保持 false —— 「没问到」不许读成「满足」。
func TestCoreRuntimeCarriesTheVerifyConditionsFromRuntimeState(t *testing.T) {
	report := stats.Report{TunnelHealthy: true}
	state := supervisor.RuntimeState{RoutesInstalled: true, DNSListening: true, UDPRequired: true, UDPReady: true}

	got := coreRuntimeFrom(report, state, nil)
	if !got.RoutesInstalled || !got.DNSListening || !got.UDPRequired || !got.UDPReady {
		t.Fatalf("verify 的条件没搬过来: %+v", got)
	}

	got = coreRuntimeFrom(report, supervisor.RuntimeState{}, errors.New("runtime state unavailable"))
	if got.RoutesInstalled || got.DNSListening || got.UDPReady || !got.Reachable || !got.TunnelHealthy {
		t.Fatalf("RuntimeState 问不出来时条件必须保持 false 而其余照旧: %+v", got)
	}
}
