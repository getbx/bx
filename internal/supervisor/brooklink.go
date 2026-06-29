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

// serverHostFromLink 从 vless:// / brook:// link 或裸 host:port 中取出服务器主机(IP/域名)。
// 用于给路由加 bypass,避免到服务器的连接被 tun 再次捕获成环。
func serverHostFromLink(server string) (string, error) {
	// authority 里带 host 的 scheme(vless / hysteria2 / hy2):直接取 url host。
	if strings.HasPrefix(server, "vless://") || strings.HasPrefix(server, "hysteria2://") || strings.HasPrefix(server, "hy2://") || strings.HasPrefix(server, "trojan://") {
		u, err := url.Parse(server)
		if err != nil {
			return "", fmt.Errorf("解析 link: %w", err)
		}
		host := u.Hostname()
		if host == "" {
			return "", fmt.Errorf("link 缺 host")
		}
		return host, nil
	}
	if strings.HasPrefix(server, "brook://") {
		u, err := url.Parse(server)
		if err != nil {
			return "", fmt.Errorf("解析 brook link: %w", err)
		}
		q := u.Query()
		endpoint := q.Get("server")
		if endpoint == "" && u.Host != "" {
			endpoint = q.Get(u.Host)
		}
		if endpoint == "" {
			return "", fmt.Errorf("brook link 缺 server/%s endpoint 参数", u.Host)
		}
		return hostFromEndpoint(endpoint)
	}
	return hostFromEndpoint(server)
}

func hostFromEndpoint(endpoint string) (string, error) {
	if u, err := url.Parse(endpoint); err == nil && u.Host != "" {
		host := u.Hostname()
		if host == "" {
			return "", fmt.Errorf("拆分 endpoint %q: host 为空", endpoint)
		}
		return host, nil
	}
	host, _, err := net.SplitHostPort(endpoint)
	if err != nil {
		if strings.Count(endpoint, ":") == 0 && endpoint != "" {
			return endpoint, nil
		}
		return "", fmt.Errorf("拆分 endpoint %q: %w", endpoint, err)
	}
	return host, nil
}
