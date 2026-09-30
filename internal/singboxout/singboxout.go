// Package singboxout 把一条服务器链接翻成 sing-box 的出站。
//
// 它原在 tunnel(vlesslink.go),而 tunnel 起子进程(os/exec)—— 手机端要在 App 里自己把
// 用户粘贴的链接变成 libbox 配置,就不能依赖 tunnel。判据只在这里一份:tunnel 的
// vlessLink 内嵌 Vless、SingboxOutbound 是 Outbound 的薄壳。本包是纯判据(purity_test.go)。
package singboxout

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/getbx/bx/internal/linkkind"
)

// ErrUnsupported:这个传输还没有给进程内 sing-box(手机端 libbox)用的出站。
var ErrUnsupported = errors.New("this transport has no in-process sing-box outbound yet")

// Outbound 给出 link 对应的 sing-box 出站(tag 由调用方定)。这一期只有 reality;其余传输
// 报 ErrUnsupported,不猜。
func Outbound(link, tag string) (map[string]any, error) {
	switch linkkind.Kind(link) {
	case linkkind.KindReality:
		v, err := ParseVless(link)
		if err != nil {
			return nil, err
		}
		return v.Outbound(tag), nil
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupported, linkkind.Kind(link))
	}
}

// Vless 是从 vless:// 分享链接里解出的 REALITY 参数(用于生成 sing-box 客户端配置)。
type Vless struct {
	UUID        string
	Host        string
	Port        int
	PublicKey   string // reality public key (pbk)
	ShortID     string // reality short id (sid)
	SNI         string // 借用的真实站点域名 (sni)
	Flow        string // 一般为 xtls-rprx-vision
	Fingerprint string // uTLS 指纹 (fp);空时默认 chrome
}

// ParseVless 解析 vless://uuid@host:port?security=reality&pbk=&sid=&sni=&flow=&fp= 形式的链接。
// 只接受 security=reality;缺 uuid/host/pbk/sid/sni 视为非法。
func ParseVless(s string) (Vless, error) {
	var v Vless
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

// Outbound 是 vless-reality 出站本身。桌面(tunnel 起的 sing-box 子进程)与手机(libbox)共用这一份。
func (v Vless) Outbound(tag string) map[string]any {
	return map[string]any{
		"type":        "vless",
		"tag":         tag,
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
	}
}
