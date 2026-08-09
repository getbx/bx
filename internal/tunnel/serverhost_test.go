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

func TestServerHostRejectsLinkWithoutHost(t *testing.T) {
	if _, err := ServerHost("vless://uuid@:443"); err == nil {
		t.Fatal("缺 host 的链接必须报错 —— 它会让 bypass 少一条,而少一条就是成环")
	}
}
