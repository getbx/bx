package tunnel

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// hysteria2Link 是从 hysteria2:// / hy2:// 分享链接里解出的参数(用于生成 sing-box hysteria2 出站)。
// hysteria2 基于 QUIC/UDP,丢包高 RTT 链路(蜂窝)上比 TCP 快——bx 把它作为「UDP/速度档」。
type hysteria2Link struct {
	Password     string
	Host         string
	Port         int
	SNI          string // 借用/真实证书域名;缺省回退 host
	Insecure     bool   // 自签证书时 true(跳过校验)
	Obfs         string // salamander 或空
	ObfsPassword string
}

// parseHysteria2Link 解析 hysteria2://password@host:port?sni=&obfs=&obfs-password=&insecure= 形式。
func parseHysteria2Link(s string) (hysteria2Link, error) {
	var h hysteria2Link
	if !strings.HasPrefix(s, "hysteria2://") && !strings.HasPrefix(s, "hy2://") {
		return h, fmt.Errorf("不是 hysteria2:// / hy2:// 链接")
	}
	u, err := url.Parse(s)
	if err != nil {
		return h, fmt.Errorf("解析 hysteria2 链接: %w", err)
	}
	h.Password = u.User.Username()
	if h.Password == "" {
		return h, fmt.Errorf("hysteria2 链接缺 password")
	}
	h.Host = u.Hostname()
	if h.Host == "" {
		return h, fmt.Errorf("hysteria2 链接缺 host")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port <= 0 || port > 65535 {
		return h, fmt.Errorf("hysteria2 链接端口非法: %q", u.Port())
	}
	h.Port = port
	q := u.Query()
	h.SNI = q.Get("sni")
	if h.SNI == "" {
		h.SNI = h.Host // 缺省借 host 当 SNI
	}
	if v := q.Get("insecure"); v == "1" || v == "true" {
		h.Insecure = true
	}
	h.Obfs = q.Get("obfs")
	h.ObfsPassword = q.Get("obfs-password")
	return h, nil
}

// singboxConfig 生成最小 sing-box 客户端配置:本地 socks 入站 + hysteria2 出站。
// 与 vless/brook 同构:bx 数据面只连本地 socks。httpAddr 非空时额外开 HTTP 代理。
func (h hysteria2Link) singboxConfig(socksAddr, httpAddr string) ([]byte, error) {
	inbounds, err := socksInbounds(socksAddr, httpAddr)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"type":        "hysteria2",
		"tag":         "hy2-out",
		"server":      h.Host,
		"server_port": h.Port,
		"password":    h.Password,
		"tls": map[string]any{
			"enabled":     true,
			"server_name": h.SNI,
			"insecure":    h.Insecure,
		},
	}
	if h.Obfs != "" {
		out["obfs"] = map[string]any{"type": h.Obfs, "password": h.ObfsPassword}
	}
	cfg := map[string]any{
		"log":       map[string]any{"level": "warn", "timestamp": false},
		"inbounds":  inbounds,
		"outbounds": []any{out},
	}
	return json.MarshalIndent(cfg, "", "  ")
}
