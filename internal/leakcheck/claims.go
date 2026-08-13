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
func isPublicPrefix(prefix netip.Prefix) bool {
	addr := prefix.Addr()
	if !addr.Is4() || addr.IsPrivate() || addr.IsLoopback() ||
		addr.IsLinkLocalUnicast() || addr.IsMulticast() || addr.IsUnspecified() {
		return false
	}
	cgnat := netip.MustParsePrefix("100.64.0.0/10")
	return !(prefix.Bits() >= cgnat.Bits() && cgnat.Contains(addr))
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
