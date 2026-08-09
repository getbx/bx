package tunnel

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// ServerHost 从 vless:// / brook:// link 或裸 host:port 中取出服务器主机(IP/域名)。
// 用于给路由加 bypass,避免到服务器的连接被 tun 再次捕获成环。
func ServerHost(server string) (string, error) {
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
	// ss:// 的 authority 是 base64(method:password),url.Parse 取不到 host,走专用解析。
	if strings.HasPrefix(server, "ss://") {
		return SSHost(server)
	}
	// vmess:// 是 base64-JSON,同理走专用解析。
	if strings.HasPrefix(server, "vmess://") {
		return VmessHost(server)
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
