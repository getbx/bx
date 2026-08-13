package leakcheck

import (
	"net/netip"
	"sort"
	"strings"
)

// TunnelClaim 是「某条不属于 bx 的隧道,在路由表里 claim 了什么」。
type TunnelClaim struct {
	Interface string
	// ClaimsPublicSpace:它 claim 的段里有公网地址 —— **它在抢你的流量**。
	ClaimsPublicSpace bool
	// Prefixes 是它 claim 的段,按字符串排序,供证据区原样出示。
	Prefixes []string
}

// isPublicPrefix 判断一个前缀是不是公网可路由空间。
//
// 与 routeEscapesTunnel 共用同一套排除项(私网 / CGNAT / loopback / link-local /
// 组播),**因为那是同一个问题的两面**:那边问「有没有人把公网流量从隧道外面送走」,
// 这边问「有没有别的隧道把公网流量收走」。
// nonPublicRanges 是**整段都不是公网可路由目的地**的地址块。
//
// 判据必须是「这个前缀**落在**其中一段里」,不是「它的网络地址长得像其中一段」——
// 后者是 `Overlaps` 那类错误的另一种写法,而这里踩过一次更隐蔽的版本(见下)。
var nonPublicRanges = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),      // RFC1122「本网络」
	netip.MustParsePrefix("10.0.0.0/8"),     // RFC1918
	netip.MustParsePrefix("172.16.0.0/12"),  // RFC1918
	netip.MustParsePrefix("192.168.0.0/16"), // RFC1918
	netip.MustParsePrefix("100.64.0.0/10"),  // CGNAT
	netip.MustParsePrefix("127.0.0.0/8"),    // loopback
	netip.MustParsePrefix("169.254.0.0/16"), // link-local
	netip.MustParsePrefix("224.0.0.0/4"),    // 组播
	netip.MustParsePrefix("240.0.0.0/4"),    // 保留
}

// isPublicPrefix 判断一个前缀是不是公网可路由空间。
//
// **`0.0.0.0/2` 是公网,而它一度被静默放过。** 早先这里写的是
// `addr.IsUnspecified()` —— 那个判断本意是排除 `0.0.0.0` 这个**主机地址**,却作用在
// 前缀的**网络地址**上,于是 0.0.0.0/1、/2、/3 全被否掉,而它们覆盖 1.0.0.0/8、
// 8.8.8.8 这些实打实的公网。后果分两处,都不小:TunnelVision 那条会漏掉
// `0.0.0.0/2` —— **那正是它唯一要抓的形状**;隧道分类会把只 claim `0.0.0.0/1` 的
// 全隧道 VPN 当成叠加。
//
// 这个洞是一次变异**没有**转红时查出来的,不是读代码读出来的。
//
// 与 routeEscapesTunnel 共用同一个判据,因为那是同一个问题的两面:那边问「有没有人
// 把公网流量从隧道外面送走」,这边问「有没有别的隧道把公网流量收走」。
// globalUnicastV6 是 v6 的公网可路由空间。v6 反过来用**白名单**:全球单播只有这一段,
// 而「不是它」的东西(链路本地 / ULA / 组播 / loopback)零散得多,列黑名单容易漏。
var globalUnicastV6 = netip.MustParsePrefix("2000::/3")

func isPublicPrefix(prefix netip.Prefix) bool {
	addr := prefix.Addr()
	if addr.Is6() && !addr.Is4In6() {
		// **`::/0` 与 `::/1` 是公网**:它们覆盖 2000::/3,正是抢流量的形状。
		// 判据是「这个前缀与全球单播有交集」,而不是「它落在全球单播里」——
		// 与 v4 那边方向相反,因为 v6 这里用的是白名单。
		if prefix.Bits() <= globalUnicastV6.Bits() {
			return prefix.Overlaps(globalUnicastV6)
		}
		return globalUnicastV6.Contains(addr)
	}
	if !addr.Is4() {
		return false
	}
	for _, block := range nonPublicRanges {
		// 只有**整个前缀都落在**非公网块里才不算公网。一个更宽的前缀(0.0.0.0/2)
		// 盖住某个非公网块(0.0.0.0/8)不影响它仍然覆盖大量公网。
		if prefix.Bits() >= block.Bits() && block.Contains(addr) {
			return false
		}
	}
	return true
}

// ClassifyTunnels 按**行为**给别的隧道分类,不按产品名。
//
// **产品名什么都没告诉你。** 同一个产品的分类会随配置翻转:Tailscale 开了 exit node
// 就抢默认路由(竞争者),不开就只 claim 100.64/10(叠加的租户);WireGuard 的
// `AllowedIPs = 10.0.0.0/8` 是租户、`= 0.0.0.0/0` 是竞争者。而新产品永远追不完,
// 进程名/接口名在 macOS 上又分不开(bx 与 Tailscale 都叫 utunN)。
//
// 可观测、自我修正的判据只有一个:**它在路由表里 claim 了公网空间没有。**
// 用户一开一关 exit node,这个判定当场跟着变,不需要任何人去更新一张清单。
//
// 那张租户表(internal/overlay)因此换了职责:它回答的是「**这个产品需要什么照顾**」
// (DERP 主机名、MagicDNS 解析器地址)—— 那部分推不出来,只能查表;
// 而「它此刻是不是在跟我抢」由这里的行为判定回答。
func ClassifyTunnels(local LocalFacts) []TunnelClaim {
	bxTun := strings.ToLower(strings.TrimSpace(local.BXTunInterface))
	byIface := map[string]*TunnelClaim{}

	for _, entry := range local.Routes {
		iface := strings.ToLower(strings.TrimSpace(entry.Interface))
		if iface == "" || iface == bxTun || !IsTunnelInterface(iface) {
			continue
		}
		if routeIsBlocking(entry.Flags) {
			// 阻断路由不是 claim:它没有把流量收走,而是把它扔掉。
			// (bx 自己的 v6 屏障就是一对 reject 的 ::/1 + 8000::/1。)
			continue
		}
		if routeIsInterfaceScoped(entry.Flags) {
			// 作用域路由只对已经绑定到该接口的流量生效,不参与一般流量的竞争。
			// 真机 macOS 的 v6 表里有六条这样的 default 挂在 utun1..utun6 上 ——
			// 不排除就是六条误报。
			continue
		}
		prefix, err := ParseRouteDestination(entry.Destination)
		if err != nil {
			continue
		}
		claim := byIface[iface]
		if claim == nil {
			claim = &TunnelClaim{Interface: iface}
			byIface[iface] = claim
		}
		claim.Prefixes = append(claim.Prefixes, prefix.String())
		if isPublicPrefix(prefix) {
			claim.ClaimsPublicSpace = true
		}
	}

	out := make([]TunnelClaim, 0, len(byIface))
	for _, claim := range byIface {
		sort.Strings(claim.Prefixes)
		out = append(out, *claim)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Interface < out[j].Interface })
	return out
}

// competingTunnels 只返回**正在抢公网流量**的那些。
func competingTunnels(local LocalFacts) []TunnelClaim {
	var out []TunnelClaim
	for _, claim := range ClassifyTunnels(local) {
		if claim.ClaimsPublicSpace {
			out = append(out, claim)
		}
	}
	return out
}
