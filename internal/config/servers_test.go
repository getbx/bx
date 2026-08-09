package config

import (
	"strings"
	"testing"

	"github.com/getbx/bx/internal/blink"
)

func parseOrFail(t *testing.T, y string) *Config {
	t.Helper()
	c, err := Parse([]byte(y))
	if err != nil {
		t.Fatalf("Parse 报错: %v", err)
	}
	return c
}

func TestServersResolveCurrent(t *testing.T) {
	c := parseOrFail(t, `
servers:
  - name: hk
    link: vless://u@1.1.1.1:443
    udp: hysteria2://p@1.1.1.1:443
  - name: tokyo
    link: vless://u@2.2.2.2:443
current: tokyo
`)
	if c.Server != "vless://u@2.2.2.2:443" {
		t.Fatalf("Server 必须是 current 那台, got %q", c.Server)
	}
	if c.UDP.Transport != "" {
		t.Fatalf("tokyo 没配 udp,UDP 槽必须为空(回落到主传输), got %q", c.UDP.Transport)
	}
	// runFailover 靠 len(Transports)>1 启动。清单绝不能喂给它 —— 那正是用户
	// 明确拒绝的「连着的时候自动切」。
	if len(c.Transports) != 1 {
		t.Fatalf("Transports 必须只有当前那一条(否则会启动自动容灾), got %v", c.Transports)
	}
}

func TestServersDefaultCurrentIsFirst(t *testing.T) {
	c := parseOrFail(t, `
servers:
  - name: hk
    link: vless://u@1.1.1.1:443
`)
	if c.Current != "hk" || c.Server != "vless://u@1.1.1.1:443" {
		t.Fatalf("没写 current 时应取第一台, got current=%q server=%q", c.Current, c.Server)
	}
}

func TestServersCurrentMustExist(t *testing.T) {
	_, err := Parse([]byte(`
servers:
  - name: hk
    link: vless://u@1.1.1.1:443
current: nope
`))
	if err == nil || !strings.Contains(err.Error(), "current") {
		t.Fatalf("current 指向不存在的服务器必须报错, got %v", err)
	}
}

func TestServersAndTransportsAreMutuallyExclusive(t *testing.T) {
	_, err := Parse([]byte(`
transports:
  - vless://u@1.1.1.1:443
servers:
  - name: hk
    link: vless://u@1.1.1.1:443
`))
	if err == nil || !strings.Contains(err.Error(), "transports") {
		t.Fatalf("servers 与 transports 必须互斥 —— 两者都是「用哪条传输」的主人, got %v", err)
	}
}

func TestServersRejectDuplicateNamesCaseInsensitively(t *testing.T) {
	_, err := Parse([]byte(`
servers:
  - name: hk
    link: vless://u@1.1.1.1:443
  - name: HK
    link: vless://u@2.2.2.2:443
`))
	if err == nil {
		t.Fatal("重名(大小写不敏感)必须报错:否则 `bx server use hk` 指向哪台是掷骰子")
	}
}

func TestLegacySingleServerStillWorks(t *testing.T) {
	c := parseOrFail(t, `
server: vless://u@1.1.1.1:443
udp:
    transport: hysteria2://p@1.1.1.1:443
`)
	if c.Server != "vless://u@1.1.1.1:443" || c.UDP.Transport != "hysteria2://p@1.1.1.1:443" {
		t.Fatalf("旧配置必须一字不改照常工作, got %q / %q", c.Server, c.UDP.Transport)
	}
	if len(c.Servers) != 0 {
		t.Fatalf("旧配置不该凭空长出 servers 清单, got %v", c.Servers)
	}
}

func TestValidateServerName(t *testing.T) {
	for _, bad := range []string{"", "  ", "a b", "东京", strings.Repeat("x", 33), "a/b"} {
		if err := ValidateServerName(bad); err == nil {
			t.Fatalf("非法名字 %q 必须被拒", bad)
		}
	}
	for _, ok := range []string{"hk", "tokyo-2", "195.133.192.92", "a_b.c-1"} {
		if err := ValidateServerName(ok); err != nil {
			t.Fatalf("合法名字 %q 被拒: %v", ok, err)
		}
	}
}

func TestDeriveServerNameFromLink(t *testing.T) {
	got, err := DeriveServerName("vless://u@195.133.192.92:443")
	if err != nil {
		t.Fatal(err)
	}
	if got != "195.133.192.92" {
		t.Fatalf("没给 --name 时应取主机名, got %q", got)
	}
}

// 用户实际粘贴的从来不是裸 vless://,而是 `bx server install` / `bx invite` 生成的
// bx:// 换壳链接 —— 也就是说清单里**每一项**的真实形状都是它。
//
// 而 Parse 会对同一条链接解两次壳:resolveServers 解一次,其后既有的
// 「优先 transports」分支再解一次。这依赖 decodeServerLink 对已解壳链接的幂等性,
// 而那份幂等性今天没有任何测试守着 —— 一次对 decodeServerLink 的重构就能悄悄打破它,
// 后果是所有用户粘贴的链接全部失效。
func TestServersEntryAcceptsBxLinkWrapper(t *testing.T) {
	const raw = "vless://u@195.133.192.92:443?sni=www.cloudflare.com"
	const rawUDP = "hysteria2://p@195.133.192.92:443?obfs=salamander"
	c := parseOrFail(t, "servers:\n  - name: tokyo\n    link: "+blink.Encode(raw)+
		"\n    udp: "+blink.Encode(rawUDP)+"\n")
	if c.Server != raw {
		t.Fatalf("bx:// 壳必须被解开且只解一次, got %q 想要 %q", c.Server, raw)
	}
	if c.UDP.Transport != rawUDP {
		t.Fatalf("udp 的 bx:// 壳同样要解开, got %q 想要 %q", c.UDP.Transport, rawUDP)
	}
	if c.Servers[0].Link != raw {
		t.Fatalf("清单里存的也应是解开后的链接(下游 bypass 与切换都读它), got %q", c.Servers[0].Link)
	}
}
