package cli

import (
	"fmt"

	"github.com/getbx/bx/internal/guardian"
)

// 升级欠条(「上一次升级欠你一次『把保护起回来』」)的**存储**归
// internal/guardian 的 Store 所有(见 guardian.Paths.UpgradeIntent):
//
//   - 它与 desired 是兄弟状态、同住 /var/lib/bx;
//   - 销账必须发生在**每一条**「用户说 off」的路径上,而那些路径的汇合点是
//     guardian.Manager.Down —— 菜单的 Turn Off 直接走 socket(POST /v1/down),
//     根本不经过 internal/cli。把文件读写留在 cli 里,就只能在 CLI 这一跳打补丁,
//     而那正是上一轮漏掉菜单的原因。
//
// 留在 cli 这边的只有**策略**:怎么把欠条与 desired 合成一个答案,以及强制拆除
// 这条 Manager.Down 够不到的逃生路径上的销账。

func openUpgradeIntentStore() *guardian.Store { return guardian.OpenDefaultStore() }

func loadUpgradeIntent() (bool, bool) { return openUpgradeIntentStore().LoadUpgradeIntent() }

func saveUpgradeIntent(desiredOn bool) error {
	return openUpgradeIntentStore().SaveUpgradeIntent(desiredOn)
}

func clearUpgradeIntent() error { return openUpgradeIntentStore().ClearUpgradeIntent() }

// resolveUpgradeDesiredOn 把「上一次未完成升级留下的欠条」与「Guardian 此刻的
// desired」合成一个答案。
//
// 只要任一方说 on 就是 on:欠条存在说明上一次升级停过保护而没能起回来,这时
// Guardian 的 desired 恰恰是**那次失败自己写下的 off**,不能拿它当用户的意思。
// 反过来,用户在两次尝试之间自己 bx up 了(欠条是 off 而 store 是 on)也一样要
// 起回来。
//
// 「欠条会不会压过用户后来明确的『关掉』?」不会 —— 用户一旦真的关掉保护,
// Manager.Down 就把欠条销了,这里根本读不到它。
func resolveUpgradeDesiredOn(storeDesiredOn, pendingDesiredOn, pendingPresent bool) bool {
	if pendingPresent {
		return pendingDesiredOn || storeDesiredOn
	}
	return storeDesiredOn
}

// forgetUpgradeDebtOnExplicitOff 在用户**明确要求关闭保护**之后抹掉欠条。
//
// 正常情况下销账发生在 guardian.Manager.Down(所有「说 off」的路径都汇合于此)。
// 这里补的是它够不到的一条:`bx down` 的强制拆除逃生路径 —— Guardian socket 不
// 可达或事务失败时,forcedMacOSTeardown 直接 bootout 服务,Manager.Down 从未被
// 调用过,而用户同样明确说了「我现在不要保护」。
//
// 抹不掉只警告,绝不让「关闭」这条路因为一个记账文件而失败 —— 停止不得依赖
// 先成功做成别的事。
func forgetUpgradeDebtOnExplicitOff(clear func() error, warn func(string)) {
	if clear == nil {
		return
	}
	if err := clear(); err != nil && warn != nil {
		warn(fmt.Sprintf("! 未能清除升级标记(%v):下次 app-install 可能会多恢复一次保护", err))
	}
}
