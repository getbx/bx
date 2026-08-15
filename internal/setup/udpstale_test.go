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

// **配置里存的是 bx:// 换壳,而 Core 只认内层链接。**
//
// 真机(2026-08-14):把换壳链接直接交给 /v0/server,它解析不出主机、装不了
// bypass,于是正确地拒绝了切换(以免成环)—— 而用户看到的是一句关于 base64
// 的错误。每个把链接交给 Core 的地方都要先过 DecodeLink。
func TestDecodeLinkUnwrapsBxShell(t *testing.T) {
	inner := "vless://u@1.2.3.4:443?security=reality"
	if got := DecodeLink(blink.Encode(inner)); got != inner {
		t.Fatalf("换壳没解开:%q", got)
	}
	if got := DecodeLink(inner); got != inner {
		t.Fatalf("裸链接被改动了:%q", got)
	}
	if got := DecodeLink(""); got != "" {
		t.Fatalf("空串:%q", got)
	}
	if got := DecodeLink("  " + inner + "  "); got != inner {
		t.Fatalf("空白没去掉:%q", got)
	}
}

// **每一种链接都要解得出端口,而不只是 vless。**
//
// 这条是 2026-08-15 review 时补的,补的是一个真 bug:第一版 LinkPort 自己
// url.Parse,于是 vmess、legacy ss、brook 三种全返回 0(而 LinkHost 对它们都
// 解得出来)。探测于是跑去测 443,把一台跑在 5443 上的健康服务器报成不可达 ——
// 而 **brook 正是本项目的默认传输**。
//
// 当时的守卫只用了 vless 链接,所以它一直绿。**「测试测的是相邻的东西」**,
// 这次的形状是「只测了最容易的那种输入」。
func TestLinkPortCoversEveryScheme(t *testing.T) {
	for _, tc := range []struct {
		name string
		link string
		want int
	}{
		{"vless", "vless://uuid@203.0.113.10:9999?security=reality", 9999},
		{"hysteria2", "hysteria2://pw@203.0.113.10:8443", 8443},
		{"trojan", "trojan://pw@203.0.113.10:7443", 7443},
		{"ss SIP002", "ss://YWVzLTI1Ni1nY206cGFzcw==@203.0.113.10:6443", 6443},
		{"ss legacy base64", "ss://YWVzLTI1Ni1nY206cGFzc0AyMDMuMC4xMTMuMTA6NjQ0Mw==", 6443},
		{"vmess", "vmess://eyJ2IjoiMiIsInBzIjoieCIsImFkZCI6IjIwMy4wLjExMy4xMCIsInBvcnQiOiI1NDQzIiwiaWQiOiJ1dWlkIiwibmV0IjoidGNwIn0=", 5443},
		{"brook", "brook://server?password=x&server=203.0.113.10%3A4443", 4443},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := LinkPort(tc.link); got != tc.want {
				t.Fatalf("LinkPort = %d, want %d —— 探测会去测错端口,把健康服务器报成不可达", got, tc.want)
			}
			// 换壳之后必须仍然解得出来:配置里存的就是换壳的。
			if got := LinkPort(blink.Encode(tc.link)); got != tc.want {
				t.Fatalf("换壳后 LinkPort = %d, want %d", got, tc.want)
			}
		})
	}
}

// **解不出来必须是 0,不是一个编出来的默认值。**
// 「链接里没写端口」与「我们没解出来」是两件事,只有调用方知道哪件更要紧。
func TestLinkPortReportsZeroWhenItCannotTell(t *testing.T) {
	for _, link := range []string{"", "   ", "不是链接", "vless://uuid@203.0.113.10", "%%%"} {
		if got := LinkPort(link); got != 0 {
			t.Errorf("LinkPort(%q) = %d,解不出来时必须是 0", link, got)
		}
	}
}
