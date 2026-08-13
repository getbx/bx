//go:build linux

package supervisor

import (
	"context"
	"net"
	"net/netip"

	"github.com/getbx/bx/internal/stats"
)

// collectNetworkWarnings 是 Linux 上的共存检测。
//
// **判据刻意不 exec。** darwin 那份要 `netstat -rn` 与 `ps`,而 Linux 上同一个问题
// 用纯 Go 就答得了:一条 Tailscale overlay 的存在等价于「本机某个接口上有一个
// 100.64/10 地址」。少一次 fork,也少一条会被 PATH / busybox 差异搞坏的路径 ——
// bx 的 Linux 目标里就有 OpenWrt/musl 那种 `netstat` 行为与 GNU 不一致的环境。
func collectNetworkWarnings(ctx context.Context) []stats.Warning {
	if err := ctx.Err(); err != nil {
		return nil
	}
	if warning := linuxTailscaleWarning(); warning.Name != "" {
		return []stats.Warning{warning}
	}
	return nil
}

// linuxTailscaleWarning 只在**确知有 Tailscale 接口、而它还没拿到 overlay 地址**时
// 出声。
//
// **判据是单边的**,与检测那一套同一条纪律:接口在、地址不在 → 报;什么都问不出来
// (枚举失败)→ 不报。一条「可能有点不对劲」的告警在每台机器上常驻,会把用户训练成
// 忽略告警,而这一行的全部价值就是它平时不出现。
func linuxTailscaleWarning() stats.Warning {
	ifaces, err := net.Interfaces()
	if err != nil {
		// 问不出来不是「没问题」,但也不是可报告的异常:这一行只在**确知**
		// 有 Tailscale 而它没就绪时出声。
		return stats.Warning{}
	}
	seenTailscaleIface := false
	for _, iface := range ifaces {
		if !looksLikeTailscaleInterface(iface.Name) {
			continue
		}
		seenTailscaleIface = true
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			prefix, err := netip.ParsePrefix(addr.String())
			if err != nil {
				continue
			}
			if tailscaleOverlayV4.Contains(prefix.Addr()) {
				return stats.Warning{} // overlay 已就绪,没什么可说的
			}
		}
	}
	if !seenTailscaleIface {
		return stats.Warning{}
	}
	return stats.Warning{
		Name:     "tailscale",
		Severity: "warn",
		Detail:   "Tailscale interface is up but has no 100.64/10 address yet",
		Hint:     "wait for reconnect, or restart Tailscale after bx is on",
	}
}

var tailscaleOverlayV4 = netip.MustParsePrefix("100.64.0.0/10")

// looksLikeTailscaleInterface 认 Tailscale 在 Linux 上的接口名。
//
// 只认 `tailscale*`(tailscaled 自己建的默认名)。**刻意不认泛化的 `tun*`/`utun*`** ——
// 那些名字属于任何隧道,把别人的 OpenVPN 接口当成「Tailscale 没就绪」会产生一条
// 永远消不掉的假告警,而假告警比没有告警更糟。
func looksLikeTailscaleInterface(name string) bool {
	return len(name) >= 9 && name[:9] == "tailscale"
}
