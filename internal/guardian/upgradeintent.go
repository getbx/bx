package guardian

import (
	"context"
	"net/http"
)

// 升级欠条的销账规则:哪一种 Down 算「用户不要保护了」。
//
// 每一条「用户说 off」都汇合于 Manager.Down(菜单的 Turn Off 走 socket,CLI 的
// 干净路径走同一个 handler),所以销账挂在那里。问题是**升级自己也走这一跳**:
// sudo bx app-install 的第一步就是停保护,而它前一秒刚写下欠条 —— 不加区分的
// 话,欠条在写下的下一步就被自己那次 Down 删掉;此后装文件一失败,重试读到
// desired=off、又找不到欠条,于是「成功」地把机器永久留在无保护状态。
// (欠条此前只在 Guardian 不可达、走强制拆除时才活得下来 —— 只有 Guardian 坏了
// 它才管用,而它正是为 Guardian 好着的那种失败准备的。)
//
// 判据必须是**请求作用域**的,不能靠「写欠条 → 停保护 → 再写一遍欠条」补救:
// 那样会在 Down 返回与第二次写盘之间留下一个真实的崩溃窗口,而这个文件存在的
// 全部理由正是「中途崩了也要记得欠用户一次保护」。请求作用域的标记一个窗口都
// 不留:欠条只写一次,升级自己的那次 Down 从不删它,只有用户明确的 off 才销账。
const (
	downReasonParam   = "reason"
	downReasonUpgrade = "upgrade"
)

// downForUpgradePath 是带升级标记的 /v1/down 路径,客户端与测试共用同一份字面量。
const downForUpgradePath = "/v1/down?" + downReasonParam + "=" + downReasonUpgrade

type upgradeStopKey struct{}

func withUpgradeStop(ctx context.Context) context.Context {
	return context.WithValue(ctx, upgradeStopKey{}, true)
}

// downClearsUpgradeIntent 报告这次 Down 结束后该不该销掉升级欠条。
//
// 没有标记就销账 —— 来源不明的 off 按「用户的意思」处理,与本判据引入之前的行为
// 一致;只有明确标着「这是升级自己的一步」才保住欠条。
//
// 它跑在 Manager.Down 的 defer 里,所以只做一次 map 读:关闭是这个项目的逃生
// 路径,不得因为任何记账逻辑而失败、阻塞或 panic。
func downClearsUpgradeIntent(ctx context.Context) bool {
	if ctx == nil {
		return true
	}
	marked, _ := ctx.Value(upgradeStopKey{}).(bool)
	return !marked
}

// markUpgradeStop 把 /v1/down?reason=upgrade 翻译成请求上下文里的标记。
//
// 缺失或不认识的 reason 一律按「用户要关保护」处理(照旧销账):拼错一个查询
// 参数的代价应该是「多销一次账」,不是「悄悄保住一张会自动把保护打开的欠条」。
func markUpgradeStop(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get(downReasonParam) == downReasonUpgrade {
			r = r.WithContext(withUpgradeStop(r.Context()))
		}
		next(w, r)
	}
}
