package tunnel

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// ssLink 是从 ss:// (shadowsocks) 分享链接解出的参数(生成 sing-box shadowsocks 出站)。
type ssLink struct {
	Method   string
	Password string
	Host     string
	Port     int
}

// b64TryDecode 容忍各种 base64 变体(url/std,有无 padding)。解不出返回原串。
func b64TryDecode(s string) string {
	for _, enc := range []*base64.Encoding{base64.RawURLEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.StdEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			return string(b)
		}
	}
	return s
}

// parseSSLink 解析 ss://:SIP002(base64(method:password)@host:port)或 legacy(全 base64(method:password@host:port))。
func parseSSLink(s string) (ssLink, error) {
	var ss ssLink
	if !strings.HasPrefix(s, "ss://") {
		return ss, fmt.Errorf("不是 ss:// 链接")
	}
	rest := strings.TrimPrefix(s, "ss://")
	if i := strings.IndexByte(rest, '#'); i >= 0 { // 去 #tag
		rest = rest[:i]
	}
	if i := strings.IndexByte(rest, '?'); i >= 0 { // 去 ?plugin 等
		rest = rest[:i]
	}
	var methodPass, hostPort string
	if at := strings.LastIndexByte(rest, '@'); at >= 0 {
		// SIP002:userinfo@host:port,userinfo 是 base64(method:password)(也容忍明文)。
		methodPass = b64TryDecode(rest[:at])
		hostPort = rest[at+1:]
	} else {
		// legacy:全段 base64(method:password@host:port)。
		dec := b64TryDecode(rest)
		at2 := strings.LastIndexByte(dec, '@')
		if at2 < 0 {
			return ss, fmt.Errorf("ss legacy 链接缺 @host:port")
		}
		methodPass = dec[:at2]
		hostPort = dec[at2+1:]
	}
	colon := strings.IndexByte(methodPass, ':') // 只切第一个冒号(密码可含冒号)
	if colon < 0 {
		return ss, fmt.Errorf("ss 链接缺 method:password")
	}
	ss.Method = methodPass[:colon]
	ss.Password = methodPass[colon+1:]
	if ss.Method == "" || ss.Password == "" {
		return ss, fmt.Errorf("ss 链接 method/password 为空")
	}
	host, portStr, err := net.SplitHostPort(hostPort)
	if err != nil {
		return ss, fmt.Errorf("ss 链接 host:port 非法 %q: %w", hostPort, err)
	}
	ss.Host = host
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		return ss, fmt.Errorf("ss 链接端口非法: %q", portStr)
	}
	ss.Port = port
	return ss, nil
}

// singboxConfig 生成最小 sing-box 客户端配置:本地 socks 入站 + shadowsocks 出站。
func (ss ssLink) singboxConfig(socksAddr, httpAddr string) ([]byte, error) {
	inbounds, err := socksInbounds(socksAddr, httpAddr)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"type":        "shadowsocks",
		"tag":         "ss-out",
		"server":      ss.Host,
		"server_port": ss.Port,
		"method":      ss.Method,
		"password":    ss.Password,
	}
	cfg := map[string]any{
		"log":       map[string]any{"level": "warn", "timestamp": false},
		"inbounds":  inbounds,
		"outbounds": []any{out},
	}
	return json.MarshalIndent(cfg, "", "  ")
}
