package supervisor

import "testing"

func TestTransportKind(t *testing.T) {
	if transportKind("vless://uid@1.2.3.4:443?security=reality") != "reality" {
		t.Error("vless should be reality")
	}
	if transportKind("brook://server?server=1.2.3.4%3A9999") != "brook" {
		t.Error("brook should be brook")
	}
	if transportKind("anything-else") != "brook" {
		t.Error("default should be brook")
	}
	if transportKind("hysteria2://pw@1.2.3.4:8443?sni=bing.com") != "hysteria2" {
		t.Error("hysteria2 should be hysteria2")
	}
	if transportKind("hy2://pw@h:443") != "hysteria2" {
		t.Error("hy2 alias should be hysteria2")
	}
	if transportKind("trojan://pw@1.2.3.4:443?sni=bing.com") != "trojan" {
		t.Error("trojan should be trojan")
	}
	if transportKind("ss://YWVzLTI1Ni1nY206cHc@1.2.3.4:8388#hk") != "shadowsocks" {
		t.Error("ss should be shadowsocks")
	}
	if transportKind("vmess://eyJhZGQiOiIxLjIuMy40In0") != "vmess" {
		t.Error("vmess should be vmess")
	}
}

func TestProxyMode(t *testing.T) {
	// router 模式优先(无视 global):只劫持 LAN 转发。
	if got := proxyMode(true, "router"); got != "router" {
		t.Errorf("router 应为 router, got %q", got)
	}
	if got := proxyMode(false, "router"); got != "router" {
		t.Errorf("router(非global)应为 router, got %q", got)
	}
	// host 模式:global→global,否则 split(china 直连)。
	if got := proxyMode(true, "host"); got != "global" {
		t.Errorf("global 应为 global, got %q", got)
	}
	if got := proxyMode(false, "host"); got != "split" {
		t.Errorf("非 global 应为 split, got %q", got)
	}
	if got := proxyMode(false, ""); got != "split" {
		t.Errorf("空 mode 非 global 应为 split, got %q", got)
	}
}
