package config

import (
	"strings"
	"testing"
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
