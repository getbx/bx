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
