// Package protectionstate 只放一样东西:「保护此刻处于什么状态」那六个字面量。
//
// **它是叶子包,自己不 import 本仓库任何东西** —— 与 internal/udpsource、
// internal/barriercidr、internal/tristate 同一个先例。
//
// 为什么要下沉:这六个值有三类消费方 —— Guardian(唯一的产地,经
// /v1/status 的 protection_state 发布出去)、internal/cli 与菜单(照它渲染)、
// 以及 **internal/leakcheck 这个纯判据包**(「bx 此刻该不该在承载公网流量」
// 就是按它判的)。而 leakcheck 按纪律不许 import guardian(那个包做 I/O),
// 于是它只能把六个字面量**自己抄一份**,再靠一条引了 guardian 的测试去钉
// 「两边还一样」。
//
// 那条守卫在 2026-09-09 变成了一个真问题:Guardian 开始 import
// internal/platformcheck(诊断采集),而 platformcheck 在 darwin 上要用
// leakcheck 的隧道占用判据 —— 于是 leakcheck 的**测试**边
// (leakcheck_test → guardian)与 guardian → platformcheck → leakcheck 首尾相接,
// `go vet ./...` 直接报 import cycle。
//
// 下沉之后判据用的就是**同一个常量**,不再有第二份拷贝:漂移在构造上不可能,
// 那条守卫因此可以退场,而不是变得更聪明 —— 这是本仓库对这类问题的最优处置
// (「第三种最好 —— 它让守卫失业」)。
//
// **值本身是对外契约**:它们进 `bx status --json` 的 protection_state,菜单侧
// (Swift)按字面量门控。改一个值等于改协议,两侧都得跟着动。
package protectionstate

const (
	Off            = "off"
	Starting       = "starting"
	Recovering     = "recovering"
	Protected      = "protected"
	Blocked        = "blocked"
	NeedsAttention = "needs_attention"
)
