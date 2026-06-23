package tunnel

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// vlessLink 是从 vless:// 分享链接里解出的 REALITY 参数(用于生成 sing-box 客户端配置)。
type vlessLink struct {
	UUID        string
	Host        string
	Port        int
	PublicKey   string // reality public key (pbk)
	ShortID     string // reality short id (sid)
	SNI         string // 借用的真实站点域名 (sni)
	Flow        string // 一般为 xtls-rprx-vision
	Fingerprint string // uTLS 指纹 (fp);空时默认 chrome
}

// parseVlessLink 解析 vless://uuid@host:port?security=reality&pbk=&sid=&sni=&flow=&fp= 形式的链接。
// 只接受 security=reality;缺 uuid/host/pbk/sid/sni 视为非法。
func parseVlessLink(s string) (vlessLink, error) {
	var v vlessLink
	if !strings.HasPrefix(s, "vless://") {
		return v, fmt.Errorf("不是 vless:// 链接")
	}
	u, err := url.Parse(s)
	if err != nil {
		return v, fmt.Errorf("解析 vless 链接: %w", err)
	}
	v.UUID = u.User.Username()
	if v.UUID == "" {
		return v, fmt.Errorf("vless 链接缺 uuid")
	}
	v.Host = u.Hostname()
	if v.Host == "" {
		return v, fmt.Errorf("vless 链接缺 host")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port <= 0 || port > 65535 {
		return v, fmt.Errorf("vless 链接端口非法: %q", u.Port())
	}
	v.Port = port
	q := u.Query()
	if q.Get("security") != "reality" {
		return v, fmt.Errorf("仅支持 security=reality, got %q", q.Get("security"))
	}
	v.PublicKey = q.Get("pbk")
	v.ShortID = q.Get("sid")
	v.SNI = q.Get("sni")
	v.Flow = q.Get("flow")
	v.Fingerprint = q.Get("fp")
	if v.PublicKey == "" || v.ShortID == "" || v.SNI == "" {
		return v, fmt.Errorf("reality 链接缺 pbk/sid/sni 之一")
	}
	if v.Flow == "" {
		v.Flow = "xtls-rprx-vision"
	}
	if v.Fingerprint == "" {
		v.Fingerprint = "chrome"
	}
	return v, nil
}

// singboxConfig 生成最小 sing-box 客户端配置:本地 socks 入站 + vless-reality 出站。
// socksAddr 形如 "127.0.0.1:10800"。bx 数据面只连这个 socks,不关心引擎内部。
// httpAddr 非空时额外开一个 HTTP 代理入站(给只认 HTTP_PROXY 的应用,如 tailscaled
// 控制面;等价于 brook 的 --http,保证切到 reality 后 tailscale 控制面仍走代理)。
func (v vlessLink) singboxConfig(socksAddr, httpAddr string) ([]byte, error) {
	host, portStr, err := net.SplitHostPort(socksAddr)
	if err != nil {
		return nil, fmt.Errorf("拆分 socks 地址 %q: %w", socksAddr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, fmt.Errorf("socks 端口 %q: %w", portStr, err)
	}
	inbounds := []any{map[string]any{
		"type":        "socks",
		"tag":         "socks-in",
		"listen":      host,
		"listen_port": port,
	}}
	if httpAddr != "" {
		hHost, hPortStr, err := net.SplitHostPort(httpAddr)
		if err != nil {
			return nil, fmt.Errorf("拆分 http 地址 %q: %w", httpAddr, err)
		}
		hPort, err := strconv.Atoi(hPortStr)
		if err != nil {
			return nil, fmt.Errorf("http 端口 %q: %w", hPortStr, err)
		}
		inbounds = append(inbounds, map[string]any{
			"type":        "http",
			"tag":         "http-in",
			"listen":      hHost,
			"listen_port": hPort,
		})
	}
	cfg := map[string]any{
		"log":      map[string]any{"level": "warn", "timestamp": false},
		"inbounds": inbounds,
		"outbounds": []any{map[string]any{
			"type":        "vless",
			"tag":         "reality-out",
			"server":      v.Host,
			"server_port": v.Port,
			"uuid":        v.UUID,
			"flow":        v.Flow,
			"tls": map[string]any{
				"enabled":     true,
				"server_name": v.SNI,
				"utls":        map[string]any{"enabled": true, "fingerprint": v.Fingerprint},
				"reality":     map[string]any{"enabled": true, "public_key": v.PublicKey, "short_id": v.ShortID},
			},
		}},
	}
	return json.MarshalIndent(cfg, "", "  ")
}
