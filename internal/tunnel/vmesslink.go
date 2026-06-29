package tunnel

import (
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// vmessLink 是从 vmess:// 分享链接(v2rayN base64-JSON 格式)解出的参数。
// vmess 老牌但仍常见;字段沿用 v2rayN 约定(add/port/id/aid/scy/net/host/path/tls/sni)。
type vmessLink struct {
	Name     string
	Add      string
	Port     int
	ID       string
	AlterID  int
	Security string // 加密(scy):auto/aes-128-gcm/chacha20-poly1305/none;缺省 auto
	Net      string // tcp/ws/grpc/h2;缺省 tcp
	Host     string // ws Host 头 / h2 host
	Path     string // ws path / grpc serviceName / h2 path
	TLS      bool
	SNI      string // 缺省回退 Host 或 Add
	ALPN     string
}

// vmessJSONStr 把 vmess JSON 里「可能是字符串也可能是数字」的字段统一取成字符串。
// v2rayN 历史上 port/aid 用字符串,部分面板用 JSON 数字,两种都要认。
func vmessJSONStr(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		// 整数化(端口/aid 都是整数),避免 443 变 "443.000000"
		return strconv.FormatInt(int64(t), 10)
	case json.Number:
		return t.String()
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", t)
	}
}

// parseVmessLink 解析 vmess://base64(json)。
func parseVmessLink(s string) (vmessLink, error) {
	var v vmessLink
	if !strings.HasPrefix(s, "vmess://") {
		return v, fmt.Errorf("不是 vmess:// 链接")
	}
	payload := strings.TrimPrefix(s, "vmess://")
	if i := strings.IndexByte(payload, '#'); i >= 0 { // 容忍尾部 #tag(少见)
		payload = payload[:i]
	}
	raw := b64TryDecode(payload)
	if raw == payload { // b64TryDecode 解不出会原样返回 → 非合法 base64
		return v, fmt.Errorf("vmess 链接 base64 解码失败")
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return v, fmt.Errorf("vmess 链接 JSON 解析失败: %w", err)
	}
	v.Name = vmessJSONStr(m["ps"])
	v.Add = vmessJSONStr(m["add"])
	if v.Add == "" {
		return v, fmt.Errorf("vmess 链接缺 add(服务器地址)")
	}
	portStr := vmessJSONStr(m["port"])
	if portStr == "" {
		return v, fmt.Errorf("vmess 链接缺 port")
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		return v, fmt.Errorf("vmess 链接端口非法: %q", portStr)
	}
	v.Port = port
	v.ID = vmessJSONStr(m["id"])
	if v.ID == "" {
		return v, fmt.Errorf("vmess 链接缺 id(uuid)")
	}
	if aid := vmessJSONStr(m["aid"]); aid != "" {
		v.AlterID, _ = strconv.Atoi(aid) // 非法/缺省视作 0(VMessAEAD)
	}
	v.Security = vmessJSONStr(m["scy"])
	if v.Security == "" {
		v.Security = "auto"
	}
	v.Net = vmessJSONStr(m["net"])
	if v.Net == "" {
		v.Net = "tcp"
	}
	v.Host = vmessJSONStr(m["host"])
	v.Path = vmessJSONStr(m["path"])
	if tls := vmessJSONStr(m["tls"]); tls == "tls" || tls == "true" || tls == "1" {
		v.TLS = true
	}
	v.SNI = vmessJSONStr(m["sni"])
	if v.SNI == "" {
		v.SNI = v.Host
	}
	if v.SNI == "" {
		v.SNI = v.Add
	}
	v.ALPN = vmessJSONStr(m["alpn"])
	return v, nil
}

// singboxConfig 生成最小 sing-box 客户端配置:本地 socks 入站 + vmess 出站(含 ws/grpc 传输与可选 TLS)。
func (v vmessLink) singboxConfig(socksAddr, httpAddr string) ([]byte, error) {
	inbounds, err := socksInbounds(socksAddr, httpAddr)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"type":        "vmess",
		"tag":         "vmess-out",
		"server":      v.Add,
		"server_port": v.Port,
		"uuid":        v.ID,
		"security":    v.Security,
		"alter_id":    v.AlterID,
	}
	// 传输层:tcp 不带 transport 块;ws/grpc/h2 各自映射。
	switch v.Net {
	case "ws":
		t := map[string]any{"type": "ws"}
		if v.Path != "" {
			t["path"] = v.Path
		}
		if v.Host != "" {
			t["headers"] = map[string]any{"Host": v.Host}
		}
		out["transport"] = t
	case "grpc":
		out["transport"] = map[string]any{"type": "grpc", "service_name": v.Path}
	case "h2", "http":
		t := map[string]any{"type": "http"}
		if v.Path != "" {
			t["path"] = v.Path
		}
		if v.Host != "" {
			t["host"] = []any{v.Host}
		}
		out["transport"] = t
	}
	if v.TLS {
		tls := map[string]any{"enabled": true, "server_name": v.SNI}
		if v.ALPN != "" {
			tls["alpn"] = strings.Split(v.ALPN, ",")
		}
		out["tls"] = tls
	}
	cfg := map[string]any{
		"log":       map[string]any{"level": "warn", "timestamp": false},
		"inbounds":  inbounds,
		"outbounds": []any{out},
	}
	return json.MarshalIndent(cfg, "", "  ")
}

// socksInbounds 构造本地 socks(+ 可选 http)入站,各传输 link 共用。
func socksInbounds(socksAddr, httpAddr string) ([]any, error) {
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
	return inbounds, nil
}
