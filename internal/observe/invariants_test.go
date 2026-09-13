package observe

import "testing"

// 不变量 5:任何「减少 bx 足迹」的操作,在任何前置条件缺失下都必须能执行。
//
// **这条测试断言不了它,而且是结构性的 —— 不是还没写。** 它仍然 t.Skip,但
// 理由已经和 2026-08-05 写下它时不一样了,所以那句 skip 的原话必须换掉。
//
// 原话是「控制面期次未实现:bx down **仍会**因前置条件缺失而拒绝执行」。
// 那是**现在时**,而三条前置各自有记录在案的修复,今天在 macOS 的 `bx down`
// 这条路上都不成立了:
//
//   - **解析不到默认网关** —— `Manager.Down` 里 `barrierContextForRuntime` 报错
//     (以及「Core 从没起来过、bypass 为空」那半)一律降级成 block-only 继续
//     拆除,不报错拒绝(manager.go 里那段注释逐条写着);
//   - **Guardian 不可达 / Guardian 答话却拒绝**(recoveryBlocked 那条永久失败)
//     —— `macOSDownLifecycleFor`(internal/cli/guardian.go)**按构造无法拒绝**:
//     它的每一条返回路径要么是干净事务成功,要么是 `forcedMacOSTeardown`,
//     没有第三种;而强制拆除自己那几步「任一步失败都继续做完剩下的」。
//
// **今天在守这件事的是 internal/cli 那几条**,不是这一条:
// TestUpgradeDownRecordsTheHoldOnEveryForcedEntrance 与
// TestUserDownRecordsDesiredOffOnEveryForcedEntrance 表驱动地走遍**每一个**
// 强制入口(Guardian 不可达 / legacy 可能在跑 / legacy 探测本身失败 /
// 干净事务失败后回落),每一格都断言 `err == nil` 且 `result.Forced` ——
// 也就是「前置缺了,拆除照样跑完」;
// TestMacOSDownForcedTeardownClearsBarrierEvenWhenEarlierStepsFail 钉住
// 「任一步失败都继续做完剩下的」;
// TestDownResultOnErrorPathsNeverRendersAsCleanSuccess 钉住反面(拆不干净时
// 不许报成已停止)。
//
// **为什么不搬到这里来断言。** invariants 1-4 住在 diverge_test.go 是因为它们
// 都是 `ObservedState` 判得出来的;第 5 条是**行为**性质,没有对应的
// `ObservedState` 形状。而 `macOSDownLifecycleFor` 是 internal/cli 里的**未导出**
// 函数,`internal/cli` 又 import 本包 —— 想在这里够到它,只能导出一个 CLI 内部
// 符号、或者把那份判断在本包重写一遍,而「同一个判断抄第二份」正是这个仓库反复
// 付学费的那件事。
//
// **仍然没有覆盖的两半,别读成已经保住了:**
//   - **非 darwin 的关闭路径本轮没有排查过**,上面那些断言一条都不适用于它们;
//   - Guardian 自己那一层的 `Manager.Down` **仍然会返回错误**
//     (barrier_install_failed)。逃生口在**上一层**:CLI 收到错误就落强制拆除。
//     换句话说不变量今天由 `bx down` 这条命令保住,不是由 daemon 的那个方法
//     保住 —— 直接打 /v1/down 的消费方(菜单)靠的是它自己那条回落。
func TestInvariantTeardownNeverRefuses(t *testing.T) {
	t.Skip("这条不变量是行为性质,observe 够不着(理由见上);今天由 internal/cli " +
		"那几条强制入口测试保住,非 darwin 未排查。见 " +
		"docs/superpowers/specs/2026-08-05-observation-layer-design.md 不变量 5")
}
