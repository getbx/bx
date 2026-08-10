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

// forEveryDecideInput 穷举 decide 的**全部**输入组合:两种 desired × 五个三态观测
// × 五道栅栏的开关,共 2×3⁵×2⁵ = 15552 种。
//
// 用穷举而不是挑几个代表性用例,是因为下面两条守卫要断言的是「**没有**哪个输入能
// 产出 X」—— 而这种全称命题挑用例挑不出来:漏掉的那一格恰好就是将来出问题的那一格。
// CaptureOK/TunnelHealthy 今天没有任何规则读,照样铺进矩阵:哪天有人给它们加了规则,
// 这两条守卫自动就覆盖了新规则,不需要谁记得回来补。
//
// **新加一道栅栏就必须加进这里的循环。** 否则「栅栏升起时不许夹带动作」这条
// 全称守卫在新栅栏上是空的 —— 它只会在 false 那一格上被验证,而 false 恰好是
// 什么都不改变的那一格。
func forEveryDecideInput(fn func(in reconcileInput)) {
	states := []observe.Tristate{observe.Unknown, observe.True, observe.False}
	for _, desired := range []DesiredState{DesiredOn, DesiredOff} {
		for _, capture := range states {
			for _, barrier := range states {
				for _, dns := range states {
					for _, socket := range states {
						for _, tunnel := range states {
							for _, blocked := range []bool{false, true} {
								for _, pathBusy := range []bool{false, true} {
									for _, uncertain := range []bool{false, true} {
										for _, hold := range []bool{false, true} {
											for _, unreadable := range []bool{false, true} {
												fn(reconcileInput{
													Desired: desired,
													Observed: observe.ObservedState{
														CaptureOK:      capture,
														BarrierPresent: barrier,
														DNSManaged:     dns,
														CoreSocket:     socket,
														TunnelHealthy:  tunnel,
													},
													RecoveryBlocked:    blocked,
													PathRecoveryBusy:   pathBusy,
													OwnershipUncertain: uncertain,
													MaintenanceHold:    hold,
													IntentUnreadable:   unreadable,
												})
											}
										}
									}
								}
							}
						}
					}
				}
			}
		}
	}
}

// **只提议「删」孤儿屏障,永远不提议「装」** —— 装屏障是整个系统里唯一一个失败后
// 用户连修复包都下不了的动作(网关探测瞬时失败 → 降级成无 server bypass 的
// block-only → 整机黑洞、隧道自己也没有恢复的通路),它绝不能出现在任何按时钟
// 驱动的判据里。
//
// 这条**刻意写成白名单而不是黑名单**。原先它只查一个字面名字 "install_barrier",
// 于是任何叫别的名字的装屏障动作都能大摇大摆走过去 —— 复审实测:加一个
// reassert_barrier 动作,整套测试全绿。而这恰是本系统里最不能靠「记得写对名字」
// 来守的一条。白名单反过来:新动作默认是不被允许的,加一项就必须在这里显式登记,
// 顺手也就被迫想一遍「这一项授权之后失败会怎样」。
func TestDecideNeverProposesAnythingOutsideTheAuthorizedSet(t *testing.T) {
	authorized := map[reconcileAction]bool{
		actionRestoreDNS:         true,
		actionClearOrphanBarrier: true,
		actionStartCore:          true,
		actionStopCore:           true,
	}
	forEveryDecideInput(func(in reconcileInput) {
		for _, action := range decide(in).Actions {
			if !authorized[action] {
				t.Fatalf("提议了未登记的动作 %q。若这是装屏障一类的动作,它绝不该存在;"+
					"若确实需要新动作,先在本测试的白名单里登记并想清楚失败后果。input=%+v",
					action, in)
			}
		}
	})
}

// 栅栏升起时,不许在报出 Held 的同时**还**夹带动作。
//
// 这条不变量原本只写在 reconcileDecision.Held 的注释里,没有任何东西钉住它。
// 复审实测:把栅栏改成只对 desired=off 短路,decide 就会返回
// Held=ownership_uncertain 外加 Actions=[start_core] —— 整包依然全绿。
// 而阶段③b 的执行方会照着 Actions 动手:一边说「被 fail-closed 拒绝挡住」,
// 一边把被拒绝的那件事交出去执行,正是这道栅栏存在的意义被绕过的样子。
func TestDecideNeverProposesActionsWhileHeldByAFence(t *testing.T) {
	forEveryDecideInput(func(in reconcileInput) {
		got := decide(in)
		if got.Held != "" && len(got.Actions) != 0 {
			t.Fatalf("栅栏 %s 已升起却仍提议 %v —— 整轮必须停摆。input=%+v", got.Held, got.Actions, in)
		}
	})
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
//
// Held 的字面值一并钉死:它按设计「原样进日志」,是 soak 期间统计「循环有多少轮
// 根本没在工作」的唯一抓手。改名不会有任何编译错误,只会让日志里的 grep 与
// 将来的统计脚本静默地数到零。
func TestDecideHoldsOnEveryFence(t *testing.T) {
	base := observe.ObservedState{DNSManaged: observe.True} // 本来会提议还原 DNS
	for _, tc := range []struct {
		name     string
		in       reconcileInput
		wantHeld string
	}{
		{"recoveryBlocked", reconcileInput{Desired: DesiredOff, Observed: base, RecoveryBlocked: true}, "recovery_blocked"},
		{"pathRecovery", reconcileInput{Desired: DesiredOff, Observed: base, PathRecoveryBusy: true}, "path_recovery_in_flight"},
		{"uncertain", reconcileInput{Desired: DesiredOff, Observed: base, OwnershipUncertain: true}, "ownership_uncertain"},
		{"maintenanceHold", reconcileInput{Desired: DesiredOff, Observed: base, MaintenanceHold: true}, "maintenance_hold"},
		{"intentUnreadable", reconcileInput{Desired: DesiredOff, Observed: base, IntentUnreadable: true}, "intent_unreadable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := decide(tc.in)
			if len(got.Actions) != 0 {
				t.Errorf("%s 升起时必须整轮不做事, got %v", tc.name, got.Actions)
			}
			if got.Held != tc.wantHeld {
				t.Errorf("Held = %q, want %q —— 这个串原样进日志,改名会让统计脚本静默数到零", got.Held, tc.wantHeld)
			}
		})
	}
}

// desired=on 那一半也必须被栅栏挡住。
//
// 上面那张表三条全是 desired=off,于是「栅栏在最前面判」这条只在半边被验证过 ——
// 复审实测:把栅栏改成只对 DesiredOff 短路,整包依旧全绿。而 desired=on 恰是
// 危险的那一半:它下面挂的是起 Core,而 ownership_uncertain 整个存在的意义就是
// 「拒绝再起一个 Core」。
func TestDecideHoldsOnFencesWhenProtectionIsWanted(t *testing.T) {
	got := decide(reconcileInput{
		Desired:            DesiredOn,
		Observed:           observe.ObservedState{CoreSocket: observe.False}, // 本来会提议起 Core
		OwnershipUncertain: true,
	})
	if len(got.Actions) != 0 {
		t.Errorf("所有权不确定时绝不能提议起 Core —— 那正是这道 fail-closed 拒绝要防的事, got %v", got.Actions)
	}
	if got.Held != "ownership_uncertain" {
		t.Errorf("Held = %q, want ownership_uncertain", got.Held)
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

// desired=off 而 Core 还在应答:提议停掉它。
//
// **这条规则此前一条测试都没有** —— 实现者如实报告了这个空缺(brief 的逐字测试块里
// 没给,他也没自行编造)。而一条没人盯着的规则,与一条不存在的规则,在回归面前
// 是同一回事。
func TestDecideProposesStoppingCoreWhenUserWantsOffButCoreAnswers(t *testing.T) {
	got := decide(reconcileInput{
		Desired:  DesiredOff,
		Observed: observe.ObservedState{CoreSocket: observe.True},
	})
	if !hasAction(got.Actions, actionStopCore) {
		t.Fatalf("desired=off 而 Core 还在应答,应当提议停它, got %v", got.Actions)
	}
}

// 而 Core 是否在跑「问不出来」时,同样什么都不做 —— 与其它规则同一条纪律。
func TestDecideDoesNotProposeStoppingCoreOnAnUnknownSocket(t *testing.T) {
	got := decide(reconcileInput{Desired: DesiredOff}) // CoreSocket 零值 = Unknown
	if hasAction(got.Actions, actionStopCore) {
		t.Fatal("问不出 Core 在不在时不许提议停它 —— 观测不到 ≠ 观测到它在")
	}
}
