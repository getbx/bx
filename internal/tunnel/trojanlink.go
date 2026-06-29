package tunnel

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// trojanLink 是从 trojan:// 分享链接解出的参数(生成 sing-box trojan 出站)。
// trojan 是 TCP/TLS 代理:伪装成普通 HTTPS,简单常见。
type trojanLink struct {
	Password    string
	Host        string
	Port        int
	SNI         string // 缺省回退 host
	Insecure    bool
	Fingerprint string // uTLS 指纹(fp),可空
}

// parseTrojanLink 解析 trojan://password@host:port?sni=&fp=&insecure=#name 形式。
func parseTrojanLink(s string) (trojanLink, error) {
	var h trojanLink
	if !strings.HasPrefix(s, "trojan://") {
		return h, fmt.Errorf("不是 trojan:// 链接")
	}
	u, err := url.Parse(s)
	if err != nil {
		return h, fmt.Errorf("解析 trojan 链接: %w", err)
	}
	h.Password = u.User.Username()
	if h.Password == "" {
		return h, fmt.Errorf("trojan 链接缺 password")
	}
	h.Host = u.Hostname()
	if h.Host == "" {
		return h, fmt.Errorf("trojan 链接缺 host")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port <= 0 || port > 65535 {
		return h, fmt.Errorf("trojan 链接端口非法: %q", u.Port())
	}
	h.Port = port
	q := u.Query()
	h.SNI = q.Get("sni")
	if h.SNI == "" {
		h.SNI = h.Host
	}
	if v := q.Get("insecure"); v == "1" || v == "true" {
		h.Insecure = true
	}
	h.Fingerprint = q.Get("fp")
	return h, nil
}

// singboxConfig 生成最小 sing-box 客户端配置:本地 socks 入站 + trojan 出站。
func (h trojanLink) singboxConfig(socksAddr, httpAddr string) ([]byte, error) {
	inbounds, err := socksInbounds(socksAddr, httpAddr)
	if err != nil {
		return nil, err
	}
	tls := map[string]any{
		"enabled":     true,
		"server_name": h.SNI,
		"insecure":    h.Insecure,
	}
	if h.Fingerprint != "" {
		tls["utls"] = map[string]any{"enabled": true, "fingerprint": h.Fingerprint}
	}
	out := map[string]any{
		"type":        "trojan",
		"tag":         "trojan-out",
		"server":      h.Host,
		"server_port": h.Port,
		"password":    h.Password,
		"tls":         tls,
	}
	cfg := map[string]any{
		"log":       map[string]any{"level": "warn", "timestamp": false},
		"inbounds":  inbounds,
		"outbounds": []any{out},
	}
	return json.MarshalIndent(cfg, "", "  ")
}
