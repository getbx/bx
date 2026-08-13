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

// tunnelDevicePrefixes 是 macOS 上「这个接口是一条隧道」的全部形状:
// utun(bx 自己、Tailscale、WireGuard、Mullvad、系统 IKEv2 都在这里)、
// ipsec、ppp(L2TP/PPTP 这类系统集成 VPN)、以及 tuntaposx 那一系的 tun/tap。
// **全仓唯一一份。** WhoOwnsTheRoute 与 isTunnelPath 都经 IsTunnelInterface 读它 ——
// 各写一份时两处会静默漂移,而它们对同一台机器给出的答案必须一致。
var tunnelDevicePrefixes = []string{
	"utun", "ipsec", "ppp", "tun", "tap", "wg",
	// **bx 自己在 Linux / Windows 上的 TUN 就叫 bx0**,而这张表原本是按 macOS
	// 命名建的,一个字母都没有它。后果在真 Linux 容器里当场看到:默认路由是 bx0,
	// 而 WhoOwnsTheRoute 判成「认不出」→ 路由逃逸那条报「没有隧道在管」——
	// 一台受保护的机器上,这条检查整个失效。
	//
	// 名字判据的局限在这里也暴露无遗:它认得 macOS 的每一种,却不认得自家的。
	// 更好的判据是问内核要接口特征(tun 设备带 point-to-point 标志),
	// 那是下一步;在那之前,至少别把自己漏掉。
	"bx",
}

// IsTunnelInterface 判断这个接口名看起来是不是一条隧道。
//
// **它回答的是「这台机器上有没有隧道可绕」,而不是「哪个 VPN」** —— 后者是
// DescribeInterface 的事,而且认不出时它老老实实说 Unidentified。这里只需要
// 前者:没有隧道的时候,「IPv6 绕过了隧道」这句指控在语法上就不成立。
//
// **判据故意宽**:偏向「认成隧道」。漏认的代价是把一条真泄漏说成「查不了」;
// 过度匹配的代价是在一台没有 VPN 的机器上多问一句 —— 而那一句仍然要求两个
// 地址族走不同接口才会出现。已知限制:一个把物理网卡留作默认路由的用户态代理
// (bx 自己在 split 模式下不是这样)认不出来,那时这条规则如实说它判不了。
// interfaceLooksLikeTunnel 先问内核,名字补它答不了的部分。
//
// **判据刻意不对称,因为证据本身就不对称**(2026-08-12 两平台实测):
//
//   - `pointtopoint` 是**可靠的正证据**:macOS 的 utun、Linux 的 IFF_TUN 都是它,
//     而且不会因为产品换个名字就失效 —— 名字表连 bx 自己的 bx0 都漏过。
//   - `broadcast` **不是可靠的负证据**:Linux 的 IFF_TAP 是二层设备,内核眼里就是
//     一段以太网,与物理网卡无法区分 —— 而 TAP 模式的 OpenVPN 确实在把流量运走。
//     所以广播段不下结论,交给名字表(`tap` 在表里)。
//   - `loopback` 两边都可靠,直接否掉。
//
// 早先这里让 broadcast 也压过名字,那与「名字表因此不能退休」这句话自相矛盾:
// tap0 会被判成物理网卡,名字表根本没机会说话。**变异测试没转红时查出来的。**
func interfaceLooksLikeTunnel(kinds map[string]InterfaceKind, name string) bool {
	switch kinds[strings.TrimSpace(name)] {
	case InterfaceTunnel:
		return true
	case InterfaceLoopback:
		return false
	}
	return IsTunnelInterface(name)
}

func IsTunnelInterface(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	for _, prefix := range tunnelDevicePrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
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
