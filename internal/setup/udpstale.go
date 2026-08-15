package setup

import (
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	"github.com/getbx/bx/internal/blink"
	"github.com/getbx/bx/internal/tunnel"
)

// StaleUDPTransport 判断换服务器之后,配置里那条 UDP 传输是不是被落在了旧机器上。
//
// 起因:`bx setup 新链接` 不带 `--udp` 时 UpdateTransports 一个字不动
// udp.transport。于是 TCP 走新服务器一切正常,而 UDP 打向一台已经不属于你的
// 机器 → 健康检查失败 → **UDP fail-closed 全阻断**。表现是网页能开、
// 微信语音和 QUIC 全废,**而 bx status 显示 Protected**(主隧道确实健康)。
//
// **判据只看「以前是不是同一台」,不猜用户想要什么**:
//   - 旧 UDP 主机 == 旧 server 主机 → 它本来就是「这台机器的 UDP」,服务器搬走
//     之后指着旧机器几乎必然是错的
//   - 两者本来就不同 → 用户是**故意**把 UDP 放在另一台(speed-within-safety),
//     那是他的配置,不是我们的事
//
// 任何一环解析不出来都返回 false:**问不出来不许报成坏消息**,那会让一次完全
// 正常的换服务器带上一句吓人的警告。
func StaleUDPTransport(before TransportsBefore, newServer string) (host string, stale bool) {
	if strings.TrimSpace(before.UDPTransport) == "" {
		return "", false
	}
	oldServerHost, ok := linkHost(before.Server)
	if !ok {
		return "", false
	}
	oldUDPHost, ok := linkHost(before.UDPTransport)
	if !ok {
		return "", false
	}
	newServerHost, ok := linkHost(newServer)
	if !ok {
		return "", false
	}
	if !strings.EqualFold(oldUDPHost, oldServerHost) {
		return "", false // 本来就在另一台 —— 用户故意的
	}
	if strings.EqualFold(oldUDPHost, newServerHost) {
		return "", false // 已经指向新服务器了
	}
	return oldUDPHost, true
}

// DecodeLink 把 bx:// 换壳解开,返回内层传输链接;不是换壳的原样返回。
//
// **Core 的 /v0/server 只认内层链接**:喂换壳的进去,它解析不出主机、装不了
// bypass,于是(正确地)拒绝切换以免成环 —— 而调用方看到的是一句关于
// base64 的错误(2026-08-14 真机)。配置里存的都是换壳的,所以每个把链接
// 交给 Core 的地方都要先过这里。
func DecodeLink(link string) string {
	link = strings.TrimSpace(link)
	if inner, err := blink.Decode(link); err == nil {
		return inner
	}
	return link
}

// LinkHost 取一条链接的服务器主机,**认 bx:// 换壳**。
//
// 导出是因为 CLI 侧渲染服务器清单要用同一份判据:只认裸链接的话,
// 配置里那些 bx:// 会让 tunnel.ServerHost 退化成「当作裸 host:port」,
// 把整串 base64 当主机名打给用户(2026-08-14 真机上就是这么显示的)。
func LinkHost(link string) (string, bool) { return linkHost(link) }

// linkHost 取一条链接的服务器主机,认 bx:// 换壳。
//
// 认换壳是必需的:用户手里的几乎全是 bx://,只认裸链接等于这条判据永不触发。
func linkHost(link string) (string, bool) {
	link = strings.TrimSpace(link)
	if link == "" {
		return "", false
	}
	if inner, err := blink.Decode(link); err == nil {
		link = inner
	}
	host, err := tunnel.ServerHost(link)
	if err != nil {
		return "", false
	}
	host = strings.TrimSpace(host)
	if !looksLikeHost(host) {
		// ServerHost 对认不出 scheme 的输入会退化成「当作裸 host:port」,
		// 于是 `%%%` 这种垃圾也会被当成一个主机名返回。这里再判一次形状:
		// **判不出来要落回「不知道」,而不是拿一个垃圾串去比较** —— 比较出
		// 「不相等」就会报一句吓人的警告,而我们其实什么都没解析出来。
		return "", false
	}
	return host, true
}

// looksLikeHost 判断这是不是一个像样的 IP 或域名。
func looksLikeHost(host string) bool {
	if host == "" {
		return false
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return true
	}
	for _, r := range host {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.' || r == '-' || r == '_':
		default:
			return false
		}
	}
	// 光是一串点或横杠不算主机名。
	return strings.ContainsAny(host, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789")
}

// LinkPort 取一条链接的服务器端口,**认 bx:// 换壳**;取不到就报 0。
//
// 探测要连的是「服务器真正在听的那个口」。**猜一个 443 会把一台跑在 9999 上的
// 服务器测成不可达**,而用户看到的是一台好机器被标成红的 —— 比不测更糟。
// 取不到时返回 0,由调用方决定怎么办(今天是回落 443 并把用到的口报出去)。
func LinkPort(link string) int {
	link = strings.TrimSpace(link)
	if link == "" {
		return 0
	}
	if inner, err := blink.Decode(link); err == nil {
		link = inner
	}
	host, err := tunnel.ServerHost(link)
	if err != nil || strings.TrimSpace(host) == "" {
		return 0
	}
	// ServerHost 只给主机,端口要自己从 authority 里取。各家链接的 authority
	// 形状不同(vless/hysteria2/trojan 是 user@host:port,ss/vmess 是 base64),
	// 所以先解壳再交给 url.Parse —— 解不出来就老实返回 0。
	u, err := url.Parse(link)
	if err != nil {
		return 0
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port <= 0 || port > 65535 {
		return 0
	}
	return port
}
