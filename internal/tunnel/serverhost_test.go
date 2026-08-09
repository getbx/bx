package tunnel

import "testing"

func TestServerHostAcrossSchemes(t *testing.T) {
	for _, tc := range []struct{ name, link, want string }{
		{"vless", "vless://uuid@195.133.192.92:443?sni=www.cloudflare.com", "195.133.192.92"},
		{"hysteria2", "hysteria2://pw@195.133.192.92:443?obfs=salamander", "195.133.192.92"},
		{"hy2 别名", "hy2://pw@example.com:8443", "example.com"},
		{"trojan", "trojan://pw@example.com:443", "example.com"},
		{"裸 endpoint", "1.2.3.4:9999", "1.2.3.4"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ServerHost(tc.link)
			if err != nil {
				t.Fatalf("ServerHost(%q) 报错: %v", tc.link, err)
			}
			if got != tc.want {
				t.Fatalf("ServerHost(%q) = %q, 想要 %q", tc.link, got, tc.want)
			}
		})
	}
}

// legacy 形式的 ss://(整个 `method:password@host:port` 被 base64 包起来)是唯一
// **只有** ss:// 那一支才解得开的输入:SIP002 形式(`ss://base64@host:port`)的主机
// 在 URL authority 里是明文,通用的 hostFromEndpoint 回退恰好也能取到,所以把
// ss:// 那一支整个删掉,原有测试照样全绿。
//
// 而解不出主机 = 那台服务器的 IP 进不了 bypass = 隧道自己的流量被劫进 TUN(成环),
// 且成环是静默的。这条支必须有一个真正依赖它的测试。
func TestServerHostNeedsTheSSArmForLegacyForm(t *testing.T) {
	const legacy = "ss://YWVzLTI1Ni1nY206aHVudGVyMkAyMDMuMC4xMTMuNzo4Mzg4"
	got, err := ServerHost(legacy)
	if err != nil {
		t.Fatalf("legacy ss:// 解析失败: %v", err)
	}
	if got != "203.0.113.7" {
		t.Fatalf("ServerHost(legacy ss://) = %q, 想要 %q", got, "203.0.113.7")
	}
}

func TestServerHostRejectsLinkWithoutHost(t *testing.T) {
	if _, err := ServerHost("vless://uuid@:443"); err == nil {
		t.Fatal("缺 host 的链接必须报错 —— 它会让 bypass 少一条,而少一条就是成环")
	}
}
