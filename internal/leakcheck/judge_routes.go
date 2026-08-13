package leakcheck

import (
	"errors"
	"net/netip"
	"strconv"
	"strings"
)

// ParseRouteDestination 把 netstat(8) 的目的地简写还原成前缀。
//
// netstat 省掉末尾的零字节,也省掉「自然」前缀长度:`10` 是 10.0.0.0/8、
// `169.254` 是 169.254.0.0/16、`172.16/12` 的地址部分要补足四段。认错一个的后果是
// 把私网段当公网段报出来,或者反过来漏掉一条真的逃逸路由。
func ParseRouteDestination(dest string) (netip.Prefix, error) {
	dest = strings.TrimSpace(dest)
	if dest == "" {
		return netip.Prefix{}, errors.New("empty destination")
	}
	// **zone 后缀必须剥掉。** v6 表里 `fe80::%en0/64` 是最常见的一类,带着 `%en0`
	// 解析必失败 —— 于是整条链路本地路由被静默丢掉,而丢掉的东西不会有人发现。
	if i := strings.IndexByte(dest, '%'); i >= 0 {
		if j := strings.IndexByte(dest[i:], '/'); j >= 0 {
			dest = dest[:i] + dest[i+j:]
		} else {
			dest = dest[:i]
		}
	}
	// v6 与点分十进制的形状完全不同,先按标准写法试一次。
	if strings.Contains(dest, ":") {
		if prefix, err := netip.ParsePrefix(dest); err == nil {
			return prefix.Masked(), nil
		}
		addr, err := netip.ParseAddr(dest)
		if err != nil {
			return netip.Prefix{}, errors.New("not an address: " + dest)
		}
		return netip.PrefixFrom(addr, addr.BitLen()), nil
	}
	if strings.Contains(dest, "/") && strings.Count(dest, ".") == 3 {
		if prefix, err := netip.ParsePrefix(dest); err == nil {
			return prefix.Masked(), nil
		}
	}

	// 剩下是 netstat 的 v4 简写:省掉末尾零字节,也省掉「自然」前缀长度。
	// `10` 是 10.0.0.0/8、`169.254` 是 169.254.0.0/16、`172.16/12` 要补足四段。
	// 认错一个的后果是把私网段当公网段报出来,或反过来漏掉一条真的逃逸路由。
	addrPart, bitsPart, hasBits := strings.Cut(dest, "/")
	octets := strings.Split(addrPart, ".")
	if len(octets) == 0 || len(octets) > 4 {
		return netip.Prefix{}, errors.New("not an IPv4 destination: " + dest)
	}
	for _, o := range octets {
		if o == "" {
			return netip.Prefix{}, errors.New("not an IPv4 destination: " + dest)
		}
		n, err := strconv.Atoi(o)
		if err != nil || n < 0 || n > 255 {
			return netip.Prefix{}, errors.New("not an IPv4 destination: " + dest)
		}
	}
	written := len(octets)
	for len(octets) < 4 {
		octets = append(octets, "0")
	}
	addr, err := netip.ParseAddr(strings.Join(octets, "."))
	if err != nil {
		return netip.Prefix{}, err
	}
	bits := written * 8 // 没写前缀长度时,写了几段就是几段
	if hasBits {
		bits, err = strconv.Atoi(bitsPart)
		if err != nil || bits < 0 || bits > 32 {
			return netip.Prefix{}, errors.New("bad prefix length in " + dest)
		}
	}
	return netip.PrefixFrom(addr, bits).Masked(), nil
}

// routeEscapesTunnel 判断这条路由会不会把公网流量从隧道之外送走。
//
// **判据的形状由真机决定。** 本机(2026-08-11)路由表里有 108 条公网 /32 经物理
// 网关,那是 bx 自己的 server bypass(其中就有 VPS 那条)。把它们报成异常,这条
// 检查在每一台正常工作的机器上都会红,于是被训练成噪声 —— 与恒绿是同一条设计风险。
//
// 所以判据是:**单主机(/32)是隧道旁路自己服务器的正常形状,不报;能捕获成片
// 流量的更宽前缀才报。** 后者恰恰是 TunnelVision(CVE-2024-3661)/ TunnelCrack
// ServerIP 要的形状 —— 攻击的目的是截走流量,不是一台主机。
//
// 前缀必须**比 split-default 更具体**(>1 位)才压得过隧道;`default` 本身在物理
// 网卡上是 split-default 的常态,不是逃逸。
func routeEscapesTunnel(entry RouteEntry, kinds map[string]InterfaceKind, addrs map[string][]string) (netip.Prefix, bool) {
	// **「这是不是一条隧道」在这个包里只能有一个答案。** 此前这里用名字判据,而
	// WhoOwnsTheRoute / ClassifyTunnels 已经改成先问内核 —— 于是同一个接口在两处
	// 得到不同答案。真 Linux 容器里当场看到的后果:一条名字表不认识的隧道
	// (bxprobe0),它**自己的子网路由**被报成「有人把公网流量从隧道外面送走」。
	if entry.Blocking || entry.Scoped || interfaceLooksLikeTunnel(kinds, entry.Interface) {
		return netip.Prefix{}, false
	}
	prefix, err := ParseRouteDestination(entry.Destination)
	if err != nil {
		return netip.Prefix{}, false
	}
	// **上下界按地址族取。** `>= 32` 是照 v4 写的(那里 /32 是单主机);v6 收进来
	// 之后,任何比 /31 更具体的目的地都会在到达公网判定之前被丢掉 —— 而 RA /
	// DHCPv6 注入的正是 /48、/56、/64,也就是说 v6 那半一条都抓不到。
	if prefix.Bits() <= 1 || prefix.Bits() >= prefix.Addr().BitLen() {
		return netip.Prefix{}, false
	}
	// **公网判定与 ClassifyTunnels 共用一个函数**(isPublicPrefix)。同一个问题的
	// 两面:那边问「有没有别的隧道把公网流量收走」,这边问「有没有人把公网流量从
	// 隧道外面送走」。此前两处各写一份,而其中一份的 `IsUnspecified()` 让
	// `0.0.0.0/2` —— 攻击最爱用的宽度之一 —— 被静默放过。
	if !isPublicPrefix(prefix) {
		return netip.Prefix{}, false
	}
	// **on-link 只有在这个接口自己持有该网段里的地址时才算「直连」。**
	//
	// 只看「没有下一跳」是不够的:RFC 3442 的 option 121 允许 router 为 0.0.0.0,
	// dhclient 装出来的正是一条没有下一跳的 `64.0.0.0/2 dev eth0 scope link`;
	// RA 注入的 v6 前缀(`proto ra`)同样没有。上一版据此豁免,等于把这条规则
	// **唯一要抓的两种形态**都放走了。
	//
	// 问不出接口地址时豁免不生效:宁可多报一条,也不放走注入路由。
	if entry.OnLink && interfaceOwnsAddressIn(addrs[entry.Interface], prefix) {
		return netip.Prefix{}, false
	}
	return prefix, true
}

// judgeRouteEscape 找「比隧道的 split-default 更具体、又指向隧道之外」的路由。
//
// 这一项**一个包都不用发**:纯本机路由表观测。它也是这套检查里唯一一条能查出
// TunnelVision / TunnelCrack ServerIP 那类**主动攻击**的规则 —— 别的检查看的是
// 流量最终从哪儿出去,而这一条看的是有没有人在你的路由表里动了手脚。
func judgeRouteEscape(local LocalFacts) Finding {
	f := Finding{ID: FindingRouteEscape, Title: "Routes around the tunnel", Section: SectionPath}

	if local.RoutesErr != "" {
		f.Summary = "Not checked: the routing table could not be read (" + local.RoutesErr + ")."
		return f
	}
	if len(local.Routes) == 0 {
		f.Summary = "Not checked: the routing table was not read."
		return f
	}
	switch WhoOwnsTheRoute(local) {
	case OwnerBX, OwnerOther:
	default:
		// 没有隧道就没有「绕过隧道」这回事。判 ok 会被读成「你很安全」。
		f.Summary = "Not checked: no tunnel is carrying this machine's traffic, " +
			"so there is nothing for a route to go around."
		return f
	}

	var escapes []string
	hosts := 0
	for _, entry := range local.Routes {
		if prefix, ok := routeEscapesTunnel(entry, local.InterfaceKinds, local.InterfaceAddrs); ok {
			escapes = append(escapes, prefix.String()+" → "+entry.Interface)
			continue
		}
		if p, err := ParseRouteDestination(entry.Destination); err == nil &&
			p.Bits() == 32 && !interfaceLooksLikeTunnel(local.InterfaceKinds, entry.Interface) && !entry.Blocking &&
			!p.Addr().IsPrivate() && !p.Addr().IsLoopback() && !p.Addr().IsLinkLocalUnicast() {
			hosts++
		}
	}
	if hosts > 0 {
		// 单主机旁路照实说,但不判好坏:它既是每条隧道旁路自己服务器的正常做法,
		// 也是 TunnelCrack ServerIP 会用的入口 —— 而这两者在路由表里长得一样。
		f.Evidence = append(f.Evidence, strconv.Itoa(hosts)+
			" single hosts are routed outside the tunnel (this is how a tunnel reaches its own servers)")
	}
	if len(escapes) > 0 {
		f.Verdict = Bad
		f.Summary = "Something has installed routes that send whole ranges of the internet " +
			"around the tunnel: " + strings.Join(escapes, ", ") + ". Traffic to those addresses " +
			"leaves with your real address. A hostile DHCP server on this network can do exactly " +
			"this (CVE-2024-3661)."
		f.Evidence = append(f.Evidence, "escaping routes: "+strings.Join(escapes, ", "))
		return f
	}
	f.Verdict = OK
	f.Summary = "No route sends whole ranges of the internet around the tunnel."
	return f
}

// interfaceOwnsAddressIn 判断这个接口是否持有该前缀里的地址。
//
// 真正的直连子网,本机在里面有地址;注入进来的网段里没有。这是把「VPS 的公网
// LAN 网段」与「option 121 / RA 注入的网段」分开的唯一可观测判据 —— 两者在路由表里
// 都是「没有下一跳」。
func interfaceOwnsAddressIn(addrs []string, prefix netip.Prefix) bool {
	for _, raw := range addrs {
		raw = strings.TrimSpace(raw)
		// 采集器给的可能是 `203.0.113.7/24`(带前缀长度)也可能是光地址,
		// 两种都要认 —— 只认一种会让豁免在半数平台上恒盲。
		if prefixed, err := netip.ParsePrefix(raw); err == nil {
			if prefix.Contains(prefixed.Addr()) {
				return true
			}
			continue
		}
		addr, err := netip.ParseAddr(raw)
		if err != nil {
			continue
		}
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}
