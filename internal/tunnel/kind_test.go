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
