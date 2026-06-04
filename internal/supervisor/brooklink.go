package supervisor

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

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
