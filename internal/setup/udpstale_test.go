package setup

import (
	"testing"

	"github.com/getbx/bx/internal/blink"
)

// **换服务器时,旧的 UDP 传输会静默留在原地,还指着旧 IP。**
//
// `bx setup 新链接` 不带 `--udp` 时,UpdateTransports 一个字不动 udp.transport
// (`if udpTransport != ""` 才写)。后果:TCP 走新服务器一切正常,而 UDP 打向
// 一台已经不属于你的机器 → 健康检查失败 → **UDP fail-closed 全阻断**。
// 表现是网页能开、微信语音和 QUIC 全废,**而 bx status 显示 Protected**
// (主隧道确实健康)。
//
// **判据不猜用户想要什么,只看「以前是不是同一台机器」**:
//   - 旧 UDP 主机 == 旧 server 主机 → 它本来就是「这台机器的 UDP」,
//     服务器搬走之后指着旧机器几乎必然是错的 → 报出来
//   - 两者本来就不同 → 用户是**故意**把 UDP 放在另一台(文档里的
//     speed-within-safety 用法)→ **一个字都不说**
func TestStaleUDPTransportOnlyFiresWhenItFollowedTheServer(t *testing.T) {
	const oldServer = "vless://u@1.1.1.1:443?security=reality"
	const newServer = "vless://u@2.2.2.2:443?security=reality"
	for _, tc := range []struct {
		name      string
		oldUDP    string
		wantStale bool
		wantHost  string
	}{
		{
			name:      "UDP 曾经跟 server 同机 —— 服务器搬走了,它还在原地",
			oldUDP:    "hysteria2://p@1.1.1.1:443?insecure=1",
			wantStale: true, wantHost: "1.1.1.1",
		},
		{
			name:   "UDP 本来就在另一台 —— 用户故意的,别多嘴",
			oldUDP: "hysteria2://p@9.9.9.9:443?insecure=1",
		},
		{
			name:   "没配 UDP 传输",
			oldUDP: "",
		},
		{
			name:   "UDP 已经指向新服务器 —— 没问题",
			oldUDP: "hysteria2://p@2.2.2.2:443?insecure=1",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host, stale := StaleUDPTransport(TransportsBefore{Server: oldServer, UDPTransport: tc.oldUDP}, newServer)
			if stale != tc.wantStale {
				t.Fatalf("stale = %v, want %v", stale, tc.wantStale)
			}
			if stale && host != tc.wantHost {
				t.Fatalf("host = %q, want %q", host, tc.wantHost)
			}
		})
	}
}

// **解析不出来时不许报「陈旧」。** 那会让一次完全正常的换服务器带上一句
// 吓人的警告,而我们其实什么都不知道 —— 与本仓库对 Tristate 的纪律同源。
func TestStaleUDPTransportStaysQuietWhenItCannotTell(t *testing.T) {
	for _, tc := range []struct{ name, oldServer, oldUDP, newServer string }{
		{"旧 server 解析不了", "not-a-link", "hysteria2://p@1.1.1.1:443", "vless://u@2.2.2.2:443"},
		{"旧 UDP 解析不了", "vless://u@1.1.1.1:443", "garbage://", "vless://u@2.2.2.2:443"},
		{"新 server 解析不了", "vless://u@1.1.1.1:443", "hysteria2://p@1.1.1.1:443", "%%%"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, stale := StaleUDPTransport(
				TransportsBefore{Server: tc.oldServer, UDPTransport: tc.oldUDP}, tc.newServer,
			); stale {
				t.Fatal("解析不出来却报了「陈旧」")
			}
		})
	}
}

// bx:// 换壳的链接也要认得出来 —— 用户手里的几乎全是这种。
func TestStaleUDPTransportUnderstandsWrappedLinks(t *testing.T) {
	oldServer := wrapForTest(t, "vless://u@1.1.1.1:443?security=reality&pbk=x&sni=www.cloudflare.com")
	oldUDP := wrapForTest(t, "hysteria2://p@1.1.1.1:443?insecure=1")
	newServer := wrapForTest(t, "vless://u@2.2.2.2:443?security=reality&pbk=x&sni=www.cloudflare.com")

	host, stale := StaleUDPTransport(TransportsBefore{Server: oldServer, UDPTransport: oldUDP}, newServer)
	if !stale {
		t.Fatal("bx:// 换壳之后就认不出来了 —— 而用户手里几乎全是这种")
	}
	if host != "1.1.1.1" {
		t.Fatalf("host = %q", host)
	}
}

func wrapForTest(t *testing.T, link string) string {
	t.Helper()
	return blink.Encode(link)
}
