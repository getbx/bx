package guardian

import (
	"context"
	"testing"
	"time"

	"github.com/getbx/bx/internal/supervisor"
)

// 真机 2026-09-04:开机后一次手动重连(recovery-17)在 transport_health 失败;
// 用户随后 `bx down` 再 `bx up`,up 的应答是 Protected,而 `bx status` 与菜单
// 却一直显示 Blocked、图标裂开 —— 同一份状态里 observed 说 capture=true、
// barrier_present=false、tunnel_healthy=true,机器其实是受保护的。
//
// 机制:observableStatus 只要**上一次**路径恢复的快照还写着 failed,就把
// Manager 自己的 Protected 改写成 Blocked;而那份快照没有任何东西会在用户
// 之后成功的 up/down 里清掉。这是「status 是记住的不是推导的」那一类失效:
// 用户的显式改动必须让更早的恢复结局失效(与「显式 up/down 无条件清挂起」
// 同一条纪律)。
func TestUserUpSupersedesAFailedPathRecoveryInObservableStatus(t *testing.T) {
	env := newProtectedManagerTestEnv(t)
	core := newFakeCorePathClient(false)
	env.manager.corePath = core
	env.manager.pathRecoveryRetryWait = func(context.Context, time.Duration) error { return context.Canceled }

	if _, err := env.manager.RequestPathRecovery(RecoveryRequest{Reason: "manual"}); err != nil {
		t.Fatal(err)
	}
	core.waitForRequest(t)
	core.release(corePathResult{snapshot: supervisor.PathRecoverySnapshot{
		State: "failed", Stage: "transport_health", ErrorCode: "transport_unavailable",
	}})
	eventually(t, func() bool { return env.manager.pathRecoveryActiveCount() == 0 })
	if got := env.manager.CurrentPathRecovery(); got.State != "failed" {
		t.Fatalf("前置不成立:恢复没有停在 failed,而是 %+v", got)
	}

	ctx := context.Background()
	if err := env.manager.Down(ctx); err != nil {
		t.Fatalf("down: %v", err)
	}
	if err := env.manager.Up(ctx); err != nil {
		t.Fatalf("up: %v", err)
	}
	if got := env.manager.Status().Protection; got != ProtectionProtected {
		t.Fatalf("前置不成立:Manager 自己在 up 之后不是 Protected 而是 %q", got)
	}

	got := observableStatus(env.manager, env.manager, LocalAPIOptions{})
	if got.Protection != ProtectionProtected {
		t.Errorf("用户 up 成功之后对外仍是 %q(被更早那次失败的恢复改写)", got.Protection)
	}
	if got.Recovery.State == "failed" {
		t.Errorf("更早那次失败的恢复快照在用户 up 之后仍然对外发布: %+v", got.Recovery)
	}
}

// 只清已结束的:正在跑的恢复由它自己发布结局。
func TestRetireCompletedPathRecoveryLeavesARunningOneAlone(t *testing.T) {
	env := newProtectedManagerTestEnv(t)
	running := RecoverySnapshot{ID: "recovery-9", State: "running", Stage: "rebind", Reason: "underlay_changed"}
	env.manager.pathRecoveryMu.Lock()
	env.manager.pathRecoveryCurrent = running
	env.manager.pathRecoveryActive = true
	env.manager.pathRecoveryMu.Unlock()

	env.manager.retireCompletedPathRecovery()

	if got := env.manager.CurrentPathRecovery(); got != running {
		t.Fatalf("正在跑的恢复被清掉了: %+v", got)
	}
}
