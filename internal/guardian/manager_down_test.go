package guardian

import (
	"context"
	"errors"
	"testing"

	"github.com/getbx/bx/internal/supervisor"
)

// TestManagerDownFallsBackToBlockOnlyBarrierWhenGatewayUnavailable covers the
// real-world failure this bug fix exists for: network trouble means the
// default gateway can't be resolved, which is exactly when the user most
// needs `bx down` to work. Down must degrade to a block-only barrier (no
// ServerBypass, IPv6 forced blocked) and keep tearing down, mirroring what
// installBarrierForRecovery already does elsewhere in this package — not
// fail the whole operation and strand the user with no way to turn Guardian
// off.
func TestManagerDownFallsBackToBlockOnlyBarrierWhenGatewayUnavailable(t *testing.T) {
	env := newProtectedManagerTestEnv(t)
	env.manager.barrierContext.Gateway = ""
	env.manager.gatewayProvider = &fakeGatewayProvider{err: errors.New("default gateway not found")}
	if len(env.manager.runtime.ServerBypass) == 0 {
		t.Fatal("test setup: runtime ServerBypass is empty, want non-empty to exercise gateway resolution")
	}

	if err := env.manager.Down(context.Background()); err != nil {
		t.Fatalf("无网关时 Down 必须成功降级,实际返回错误: %v", err)
	}

	installed := env.barrier.lastInstallContext()
	if len(installed.ServerBypass) != 0 {
		t.Errorf("installed barrier ServerBypass = %v, want empty (block-only) when gateway unresolvable", installed.ServerBypass)
	}
	if !installed.blockOnly {
		t.Error("installed barrier blockOnly = false, want true when gateway unresolvable")
	}
	if !installed.BlockIPv6 {
		t.Error("installed barrier BlockIPv6 = false, want true (block-only forces IPv6 block)")
	}

	status := env.manager.Status()
	if status.Desired != DesiredOff || status.Protection != ProtectionOff {
		t.Fatalf("status = %+v, want fully torn down to off", status)
	}
}

// Core 从没成功起来过时,runtime 里没有 server bypass —— 而这恰恰是用户最需要
// 关掉保护的时刻。
//
// 真机 2026-08-06:VPS 不通 → 隧道连不上 → "core health check timed out after 20s"
// → Core 的控制 socket 从未出现 → runtime 为空 → bx down 报
// 500 barrier_install_failed("install down barrier: server bypass required")
// → 用户看到一屏红字,最后只能 uninstall。
//
// 网关那条路径早就降级了(见上一个用例),但降级挂在了错误的失败点上:
// barrierContextForRuntime 这时是**成功**返回的,只是产出的 context 里
// ServerBypass 为空,要到 installBarrier 里才被 validateBarrierContext 拒掉。
// 关闭路径不得依赖「Core 必须先成功起来过」这个前置条件。
func TestManagerDownFallsBackToBlockOnlyBarrierWhenServerBypassMissing(t *testing.T) {
	env := newProtectedManagerTestEnv(t)
	// Core 从没把 runtime 交回来过,且健康检查一直超时 —— 与真机一致。
	env.manager.runtime = supervisor.RuntimeState{}
	env.manager.barrierContext.ServerBypass = nil
	env.health.err = errors.New("core health check timed out after 20s")

	if err := env.manager.Down(context.Background()); err != nil {
		t.Fatalf("拿不到 server bypass 时 Down 必须降级为 block-only 继续拆除,实际返回错误: %v", err)
	}

	installed := env.barrier.lastInstallContext()
	if !installed.blockOnly {
		t.Error("installed barrier blockOnly = false, want true —— 没有 bypass 就只能装 block-only")
	}
	if len(installed.ServerBypass) != 0 {
		t.Errorf("installed barrier ServerBypass = %v, want empty", installed.ServerBypass)
	}
	if !installed.BlockIPv6 {
		t.Error("block-only 屏障必须强制阻断 IPv6")
	}

	status := env.manager.Status()
	if status.Desired != DesiredOff || status.Protection != ProtectionOff {
		t.Fatalf("status = %+v, want fully torn down to off", status)
	}
	if status.LastError == "barrier_install_failed" {
		t.Error("降级成功后不得留下 barrier_install_failed")
	}
}
