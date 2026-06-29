package tunnel

import "testing"

// TestKind 锁定「scheme → 引擎」的唯一真相源。每加一种传输都要在此登记,
// 防 supervisor/setup/blink 各处派发发散(曾有 ss 加了但 setup 探测仍当 brook 的回归)。
func TestKind(t *testing.T) {
	cases := map[string]string{
		"vless://uid@1.2.3.4:443?security=reality": "reality",
		"hysteria2://pw@1.2.3.4:8443?sni=bing.com": "hysteria2",
		"hy2://pw@h:443":                           "hysteria2",
		"trojan://pw@1.2.3.4:443?sni=bing.com":     "trojan",
		"ss://YWVzLTI1Ni1nY206cHc@1.2.3.4:8388#hk": "shadowsocks",
		"vmess://eyJhZGQiOiIxLjIuMy40In0":          "vmess",
		"brook://server?server=1.2.3.4%3A9999":     "brook",
		"anything-else":                            "brook",
	}
	for link, want := range cases {
		if got := Kind(link); got != want {
			t.Errorf("Kind(%q)=%q want %q", link, got, want)
		}
	}
}

// TestIsClientLink 锁定「裸客户端链接」识别口径(六种 scheme),供 cli/blink 各处单一化。
// bx:// / blink:// 是换壳链接(由 blink 解壳),非裸链接;乱串也不是。
func TestIsClientLink(t *testing.T) {
	yes := []string{
		"vless://uid@1.2.3.4:443?security=reality",
		"hysteria2://pw@h:443", "hy2://pw@h:443",
		"trojan://pw@1.2.3.4:443",
		"ss://YWVzLTI1Ni1nY206cHc@1.2.3.4:8388",
		"vmess://eyJhZGQiOiIxLjIuMy40In0",
		"brook://server?server=1.2.3.4%3A9999",
	}
	no := []string{
		"bx://abc", "blink://abc", "anything-else", "", "http://x",
	}
	for _, l := range yes {
		if !IsClientLink(l) {
			t.Errorf("IsClientLink(%q)=false want true", l)
		}
	}
	for _, l := range no {
		if IsClientLink(l) {
			t.Errorf("IsClientLink(%q)=true want false", l)
		}
	}
}
