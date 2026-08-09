package supervisor

import (
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
