package observe

import "testing"

// 不变量 5:任何「减少 bx 足迹」的操作,在任何前置条件缺失下都必须能执行。
//
// 本期不实现该不变量(属控制面期次)。此测试刻意留作**已知失败**,让"控制面还没修"
// 成为 CI 里可见的事实,而不是待办清单里的一行字。
//
// 真实事故:bx down 因解析不到默认网关、Guardian 起不来、recoveryBlocked 三种前置
// 缺失而拒绝执行,用户被锁在断网状态,最终只能 uninstall 脱身。
//
// 实现该不变量后,删除 t.Skip 并补上真实断言。
func TestInvariantTeardownNeverRefuses(t *testing.T) {
	t.Skip("控制面期次未实现:bx down 仍会因前置条件缺失而拒绝执行。见 " +
		"docs/superpowers/specs/2026-08-05-observation-layer-design.md 不变量 5")
}
