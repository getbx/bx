package leakserve

import (
	"net"

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
