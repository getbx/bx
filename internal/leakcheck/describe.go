package leakcheck

import "strings"

// VPNService 是系统集成 VPN 的一条(来自 `scutil --nc list`)。
//
// 刻意**不含接口名**:scutil 给的是服务显示名与状态,它不告诉你哪条服务占着
// utun4。这个类型如实反映那个限制,归因规则见 DescribeInterface。
type VPNService struct {
	Name      string `json:"name"`
	Connected bool   `json:"connected"`
}

// DescribeInterface 把 `utun4` 翻成人话。**翻不出就说翻不出。**
//
// 归因只在**唯一确定**时发生:
//   - 接口就是 bx 自己的 TUN(Guardian 报的)→ 确知,直接说 bx;
//   - 接口是 utun 且**恰好只有一条**已连接的系统 VPN → 那条就是它;
//   - 其余一切 utun → "Unidentified VPN (utunN)"。
//
// **两条都连着时不许猜。** scutil 不给服务到接口的映射,挑一条填上去就是编造,
// 而一个编造出来的 VPN 名字比 "Unidentified" 有害得多:用户会照着它去关一个
// 根本没在占路由的 VPN。设计原文:「无法识别」是一个合法答案,猜不是。
//
// 非 utun 的接口(en0、bridge0……)原样返回:把物理网卡说成「无法识别的 VPN」
// 是另一种撒谎。用户态工具(Tailscale / WireGuard / Mullvad)按进程识别是下一期
// 的事;本期它们落在 "Unidentified VPN",那是诚实的。
// bxTunKnown 说的是「我们**问出来了** bx 的 TUN 是哪个」,而不是「bx 有 TUN」。
//
// 这两件事必须分开,而把它们压成一个空串会朝最糟的方向出错:Guardian 不应答时
// bxTun 是空的,如果这台机器上恰好只有一个 VPN 连着,bx **自己的** utun 就会被
// 贴上别人的名字 —— 一个猜测被当成事实报出去,正是设计里那条「『无法识别』是
// 合法答案,猜不是」明令禁止的。
//
// 反过来也不能一空就拒绝归属:保护关着、bx 确实没有 TUN 时,把 utun4 认成
// 「Work VPN」正是这个功能的主用途。所以判据是「问没问出来」,不是「空不空」。
func DescribeInterface(name string, services []VPNService, bxTun string, bxTunKnown bool) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "not observed"
	}
	if bxTunKnown && bxTun != "" && name == strings.TrimSpace(bxTun) {
		return "bx (" + name + ")"
	}
	if !strings.HasPrefix(name, "utun") {
		return name
	}
	if !bxTunKnown {
		// 问不出 bx 占着哪个 utun,就分不清眼前这个是它的还是别人的。
		return "Unidentified VPN (" + name + ")"
	}
	var connected []string
	for _, service := range services {
		if service.Connected && strings.TrimSpace(service.Name) != "" {
			connected = append(connected, service.Name)
		}
	}
	if len(connected) == 1 {
		return connected[0] + " (" + name + ")"
	}
	return "Unidentified VPN (" + name + ")"
}
