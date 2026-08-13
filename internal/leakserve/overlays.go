package leakserve

import (
	"net"
	"net/netip"

	"github.com/getbx/bx/internal/overlay"
)

// listOverlayTenants 问 internal/overlay「此刻有哪些 overlay 在跑」。
//
// **与 supervisor 那份共用同一张租户表**,而不是各写一份检测:两处各写一份时,
// 检测页会说「Tailscale 在跑」而数据面没给它开路(或反过来),两个都自认正确、
// 互相矛盾,而用户没有办法分辨谁对。
func listOverlayTenants() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		// 问不出来就当没有:这份事实只用来**解释**一个已观测到的现象,
		// 缺了它最坏的后果是少一句解释,而编一个租户出来会让用户去关错东西。
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
	var names []string
	for _, tenant := range overlay.Detect(signals) {
		names = append(names, tenant.Name)
	}
	return names
}
