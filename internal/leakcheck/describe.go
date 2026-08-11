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
func DescribeInterface(name string, services []VPNService, bxTun string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "not observed"
	}
	if bxTun != "" && name == strings.TrimSpace(bxTun) {
		return "bx (" + name + ")"
	}
	if !strings.HasPrefix(name, "utun") {
		return name
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
