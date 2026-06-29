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
}
