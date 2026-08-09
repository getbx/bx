package supervisor

import (
	"context"
	"net"
	"net/netip"

	"github.com/getbx/bx/internal/tunnel"
)

// hostToCIDRs 把服务器主机(IP 或域名)转成 bypass 用的 /32、/128 CIDR 列表。
// 域名会经系统解析(此时 tun 尚未接管,解析正常)。
func hostToCIDRs(host string) []string {
	return addrsToCIDRs(hostToAddrs(host))
}

func hostToAddrs(host string) []netip.Addr {
	if addr, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{addr.Unmap()}
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return nil
	}
	var out []netip.Addr
	for _, ip := range ips {
		if a, ok := netip.AddrFromSlice(ip); ok {
			out = append(out, a.Unmap())
		}
	}
	return out
}

// hostToAddrsCtx 是 hostToAddrs 的带上下文版本,**给刷新路径用**。
//
// hostToAddrs 走的是没有超时的 net.LookupIP;而「切换前刷新 bypass」发生在
// 控制面的 cs.mu 里,解析器一挂就把 /v0/commit、/v0/transport、/v0/rehijack
// 一起连坐。这个项目已经有过一次「用户 71 分钟关不掉保护」的事故,控制面锁
// 无上限地持有正是那个形状。故刷新路径改用可被 ctx 封顶的解析器。
//
// 启动路径不动:那时还没有控制面锁可连坐,而启动阶段的解析失败本就该让启动失败。
func hostToAddrsCtx(ctx context.Context, host string) []netip.Addr {
	if addr, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{addr.Unmap()}
	}
	var r net.Resolver
	ips, err := r.LookupIPAddr(ctx, host)
	if err != nil {
		return nil
	}
	var out []netip.Addr
	for _, ip := range ips {
		if a, ok := netip.AddrFromSlice(ip.IP); ok {
			out = append(out, a.Unmap())
		}
	}
	return out
}

func addrsToCIDRs(addrs []netip.Addr) []string {
	var out []string
	for _, addr := range addrs {
		if addr.IsValid() {
			out = append(out, netip.PrefixFrom(addr, addr.BitLen()).String())
		}
	}
	return out
}

// serverHostFromLink 已搬进 internal/tunnel(config/setup/cli 也要用它,而
// internal/tunnel 零 getbx 依赖、config 早已 import 它,搬过去无环)。
// 这里留一层薄别名,是为了不改本包内十几个调用点。
func serverHostFromLink(server string) (string, error) { return tunnel.ServerHost(server) }
