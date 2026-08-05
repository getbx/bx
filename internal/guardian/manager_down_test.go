package guardian

import (
	"context"
	"errors"
	"testing"
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
