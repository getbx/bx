package tunnel

import (
	"fmt"
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
