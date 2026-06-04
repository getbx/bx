package supervisor

import (
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
)

// hostToCIDRs 把服务器主机(IP 或域名)转成 bypass 用的 /32、/128 CIDR 列表。
// 域名会经系统解析(此时 tun 尚未接管,解析正常)。
func hostToCIDRs(host string) []string {
	if addr, err := netip.ParseAddr(host); err == nil {
		return []string{netip.PrefixFrom(addr, addr.BitLen()).String()}
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return nil
	}
	var out []string
	for _, ip := range ips {
		if a, ok := netip.AddrFromSlice(ip); ok {
			a = a.Unmap()
			out = append(out, netip.PrefixFrom(a, a.BitLen()).String())
		}
	}
	return out
}

// serverHostFromLink 从 brook:// link 或裸 host:port 中取出服务器主机(IP/域名)。
// 用于给路由加 bypass,避免 brook 到服务器的连接被 tun 再次捕获成环。
func serverHostFromLink(server string) (string, error) {
	if strings.HasPrefix(server, "brook://") {
		u, err := url.Parse(server)
		if err != nil {
			return "", fmt.Errorf("解析 brook link: %w", err)
		}
		hp := u.Query().Get("server")
		if hp == "" {
			return "", fmt.Errorf("brook link 缺 server 参数")
		}
		host, _, err := net.SplitHostPort(hp)
		if err != nil {
			return "", fmt.Errorf("拆分 server %q: %w", hp, err)
		}
		return host, nil
	}
	host, _, err := net.SplitHostPort(server)
	if err != nil {
		return "", fmt.Errorf("拆分 host:port %q: %w", server, err)
	}
	return host, nil
}
