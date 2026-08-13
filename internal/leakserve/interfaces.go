package leakserve

import (
	"net"
	"net/netip"

	"github.com/getbx/bx/internal/leakcheck"
)

// classifyInterfaceFlags 把内核给的接口标志翻译成一句判据层能用的话。
//
// **实测数据(2026-08-12,两平台):**
//
//	macOS  utun0      up|pointtopoint|multicast|running
//	macOS  en0        up|broadcast|multicast|running
//	macOS  lo0        up|loopback|multicast|running
//	Linux  IFF_TUN    pointtopoint|multicast
//	Linux  IFF_TAP    broadcast|multicast      ← 与物理网卡无法区分
//	Linux  eth0       up|broadcast|multicast|running
//
// `pointtopoint` 是**跨平台一致**的隧道正证据,而它正是名字表给不了的那部分:
// 名字表按 macOS 命名建的,连 bx 自己在 Linux/Windows 上的 bx0 都漏过。
//
// **TAP 覆盖不到**:它是二层设备,内核眼里就是一段以太网。所以这不是替代名字表,
// 是补充它 —— 谁把名字表删掉,TAP 模式的 OpenVPN 就会被判成「没有隧道」。
func classifyInterfaceFlags(flags net.Flags) leakcheck.InterfaceKind {
	switch {
	case flags&net.FlagLoopback != 0:
		return leakcheck.InterfaceLoopback
	case flags&net.FlagPointToPoint != 0:
		return leakcheck.InterfaceTunnel
	case flags&net.FlagBroadcast != 0:
		return leakcheck.InterfacePhysical
	default:
		// 什么标志都没有(macOS 的 stf0 就是)—— **不猜**。
		return leakcheck.InterfaceUnknown
	}
}

// listInterfaceKinds 问内核每个接口是什么。纯 Go,不 exec,三平台共用。
func listInterfaceKinds() map[string]leakcheck.InterfaceKind {
	ifaces, err := net.Interfaces()
	if err != nil {
		// 问不出来就返回 nil:判据层会退回名字判据,而不是把每个接口当成 Unknown
		// (那会让整条路由分析在一次枚举失败上全线失效)。
		return nil
	}
	kinds := make(map[string]leakcheck.InterfaceKind, len(ifaces))
	for _, iface := range ifaces {
		kinds[iface.Name] = classifyInterfaceFlags(iface.Flags)
	}
	return kinds
}

// listInterfaceAddrs 问内核每个接口自己持有哪些地址。纯 Go,不 exec,三平台共用。
//
// **它是 on-link 豁免能成立的前提。** 判据层要区分两种在路由表里长得一模一样的东西:
// 一台 VPS 的公网 LAN 网段(`203.0.113.0/24 dev eth0 scope link`),与 RFC 3442
// option 121 用 router 0.0.0.0 注入的网段(`64.0.0.0/2 dev eth0 scope link`)——
// 两者都没有下一跳。唯一可观测的区别是:**前者本机在里面有地址,后者没有。**
//
// 采不到就是空表,而判据层对空表的处置是「豁免不生效、照报」——
// 与本仓库其它地方同一条纪律:问不出来不等于问出了「没有」。
func listInterfaceAddrs() map[string][]string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	out := map[string][]string{}
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			if s := addr.String(); s != "" {
				out[iface.Name] = append(out[iface.Name], s)
			}
		}
	}
	return out
}

// routeIsOnLink 判断一条路由有没有下一跳。
//
// **Windows 侧原本一条都不设 OnLink**,于是每台开了原生 IPv6 的 Windows 机器上,
// 物理网卡那条 on-link 的 /64 都会被报成「有人注入了路由」—— 一条恒红的检查
// 会被用户训练成噪声,而这条检查存在的全部意义是它红的时候要被当真。
//
// 未指定地址(0.0.0.0 / ::)就是「这段直接挂在这条线上」;地址本身无效
// (`GetIPForwardTable2` 给了个读不出来的 sockaddr)时**不当作 on-link** ——
// 问不出来不许换成豁免。
func routeIsOnLink(nextHop netip.Addr) bool {
	return nextHop.IsValid() && nextHop.IsUnspecified()
}
