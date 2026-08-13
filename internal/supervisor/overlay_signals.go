package supervisor

import (
	"log"
	"net"
	"net/netip"

	"github.com/getbx/bx/internal/overlay"
)

// detectOverlayTenants 采集检测信号并问 internal/overlay「谁在跑」。
//
// **纯 Go,不 exec**:接口名与地址由 net.Interfaces 就能拿到,不需要 ifconfig/ps。
// 少一次 fork,也少一条会被 busybox/OpenWrt 的命令行差异搞坏的路径。
//
// **只在启动时采一次。** 之后才起来的 overlay 不会被认出来 —— 那是已知边界,
// 与 DERP 旁路那条同源:要动态跟上,得让 bypass/DNS 两侧都能重装,而 DNS split
// 现在是启动时一次性喂给 dnsSrv 的。写在这里,免得下一个人以为它是活的。
func detectOverlayTenants() []overlay.Tenant {
	ifaces, err := net.Interfaces()
	if err != nil {
		// 问不出来就当没有:多报一个租户的代价是给几条陌生 /32 开旁路,
		// 而这里连接口都枚举不了,任何推断都没有依据。
		log.Printf("overlay 共存:接口枚举失败,跳过租户检测:%v", err)
		return nil
	}
	signals := overlay.Signals{}
	for _, iface := range ifaces {
		signals.InterfaceNames = append(signals.InterfaceNames, iface.Name)
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if prefix, err := netip.ParsePrefix(a.String()); err == nil {
				signals.Addresses = append(signals.Addresses, prefix.Addr())
			}
		}
	}
	present := overlay.Detect(signals)
	for _, tenant := range present {
		log.Printf("overlay 共存:检测到 %s 在跑,将为它开路", tenant.Name)
	}
	return present
}
