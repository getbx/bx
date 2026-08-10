package guardian

import (
	"context"
	"net/http"
)

// 维护挂起的销账规则:哪一种 Down 算「用户不要保护了」。
//
// 每一条「用户说 off」都汇合于 Manager.Down(菜单的 Turn Off 走 socket,CLI 的
// 干净路径走同一个 handler),所以判据挂在那里。问题是**升级自己也走这一跳**:
// sudo bx app-install 的第一步就是停保护,而它前一秒刚武装了一张维护挂起。
//
// **线上的 `?reason=upgrade` 刻意原样保留。** 设计正文说这个参数会随欠条「一起
// 消失」,但取舍三与取舍四都要求它继续存在,只是它现在守的是**两件**事而不是欠条:
//   - 升级自己的那次停保护**不改写 desired**(否则干净路径这个写入点照旧撒谎,
//     而调谐器会忠实地把机器收敛到 off ——「漏一个等于没修」);
//   - 升级自己的那次停保护**不销挂起**(它前一秒才武装)。
//
// 而用户明确的 off 两件都做。判据必须是请求作用域的:靠「写 → 停 → 再写一遍」
// 补救会留下一个真实的崩溃窗口,而这两个文件存在的全部理由正是「中途崩了也要
// 记得此刻不该有保护 / 用户本来要保护」。
//
// 字面量不改还有第二个理由:升级窗口里新旧两版共存。新 CLI 对旧 Guardian 发一个
// 未知的 reason,旧 Guardian 会按「用户说 off」处理 —— 也就是今天的行为,安全;
// 换一个新词则两个方向都要额外论证。
//
// **反方向(旧 CLI × 新 Guardian)的真实后果,复审逐条对着真 Store 核过**:
// 回滚,或者一个旧安装包盖在新 Guardian 上。**盘上不是「两句话都没有」**——
// 旧 CLI 不认识维护挂起,但它会写一张升级欠条(upgrade-intent.json),而新
// Guardian 的启动恢复第一件事就是迁移它(MigrateLegacyUpgradeIntent);升级
// 自己又必然重启 Guardian(restartGuardianForUpgrade),所以那次迁移一定会跑:
// 武装一张 15 分钟的 legacy_upgrade 挂起,并把 desired 复位成 on。
//
// 顺利跑完的升级不受影响(末尾那次 `bx up` 销掉挂起、把保护起回来)。**代价
// 出现在那次升级死在末尾的 `bx up` 之前**:此后 15 分钟内的一次开机,启动恢复
// 会读到那张挂起并停手 —— 而没有迁移的话它读到 desired=on 就把保护起回来了。
// 也就是说这条兼容把「开机自愈」换成了「一次开机被压制」,15 分钟后自行结束。
// **③b 给调谐器执行权时必须重新评估这一条** —— 届时正确的处置多半是让新
// Guardian 在收到维护标记却读不到挂起时自己补一张。
const (
	downReasonParam   = "reason"
	downReasonUpgrade = "upgrade"
)

// downForUpgradePath 是带维护标记的 /v1/down 路径,客户端与测试共用同一份字面量。
const downForUpgradePath = "/v1/down?" + downReasonParam + "=" + downReasonUpgrade

type maintenanceStopKey struct{}

func withMaintenanceStop(ctx context.Context) context.Context {
	return context.WithValue(ctx, maintenanceStopKey{}, true)
}

// downIsMaintenance 报告这次 Down 是不是维护(升级)自己的一步。
//
// 没有标记就不是 —— 来源不明的 off 按「用户的意思」处理,与本判据引入之前的行为
// 一致;拼错一个查询参数的代价应该是「多销一次挂起、多写一次 desired=off」,
// 不是「悄悄压住一台机器的保护」。
//
// 它跑在 Manager.Down 的开头,所以只做一次 map 读:关闭是这个项目的逃生路径,
// 不得因为任何记账逻辑而失败、阻塞或 panic。
func downIsMaintenance(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	marked, _ := ctx.Value(maintenanceStopKey{}).(bool)
	return marked
}

// markMaintenanceStop 把 /v1/down?reason=upgrade 翻译成请求上下文里的标记。
//
// 缺失或不认识的 reason 一律按「用户要关保护」处理(照旧销挂起、照旧写 off)。
func markMaintenanceStop(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get(downReasonParam) == downReasonUpgrade {
			r = r.WithContext(withMaintenanceStop(r.Context()))
		}
		next(w, r)
	}
}
