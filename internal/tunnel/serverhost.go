package tunnel

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
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

// ServerPort 从 server link 解出端口。**与 ServerHost 逐条对应的分支**。
//
// 它必须住在这个文件里,而不是调用方那边:这里已经拥有「每种 scheme 长什么样」
// 那份知识(vless/hysteria2/trojan 的 authority 是 host:port,ss 与 vmess 的
// authority 是 base64 要走专用解析,brook 的地址在查询参数里)。
// 在别处 url.Parse 一下就想拿端口,对后三种全都得 0 —— 而那个 0 会被当成
// 「没写端口,按 443」,于是一台跑在 5443 上的健康服务器被测成不可达。
// (2026-08-15 review 实测:vmess / legacy ss / brook 三种全中,而 brook 正是本项目
// 的默认传输。)
//
// **解不出来返回 0 与一个错误**,由调用方决定怎么办 —— 绝不在这里编一个默认值:
// 「链接里没写」与「我们没解出来」是两件事,而只有调用方知道哪一件更要紧。
func ServerPort(server string) (int, error) {
	switch {
	case strings.HasPrefix(server, "vless://"), strings.HasPrefix(server, "hysteria2://"),
		strings.HasPrefix(server, "hy2://"), strings.HasPrefix(server, "trojan://"):
		u, err := url.Parse(server)
		if err != nil {
			return 0, fmt.Errorf("解析 link: %w", err)
		}
		return portFromString(u.Port())
	case strings.HasPrefix(server, "ss://"):
		ss, err := parseSSLink(server)
		if err != nil {
			return 0, err
		}
		return validPort(ss.Port)
	case strings.HasPrefix(server, "vmess://"):
		v, err := parseVmessLink(server)
		if err != nil {
			return 0, err
		}
		return validPort(v.Port)
	case strings.HasPrefix(server, "brook://"):
		u, err := url.Parse(server)
		if err != nil {
			return 0, fmt.Errorf("解析 brook link: %w", err)
		}
		q := u.Query()
		endpoint := q.Get("server")
		if endpoint == "" && u.Host != "" {
			endpoint = q.Get(u.Host)
		}
		if endpoint == "" {
			return 0, fmt.Errorf("brook link 缺 server/%s endpoint 参数", u.Host)
		}
		return portFromEndpoint(endpoint)
	}
	return portFromEndpoint(server)
}

func portFromEndpoint(endpoint string) (int, error) {
	if u, err := url.Parse(endpoint); err == nil && u.Host != "" {
		return portFromString(u.Port())
	}
	_, port, err := net.SplitHostPort(endpoint)
	if err != nil {
		return 0, fmt.Errorf("拆分 endpoint %q: %w", endpoint, err)
	}
	return portFromString(port)
}

func portFromString(s string) (int, error) {
	if s == "" {
		return 0, fmt.Errorf("link 缺端口")
	}
	port, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("端口 %q 不是数字", s)
	}
	return validPort(port)
}

func validPort(port int) (int, error) {
	if port <= 0 || port > 65535 {
		return 0, fmt.Errorf("端口 %d 越界", port)
	}
	return port, nil
}
