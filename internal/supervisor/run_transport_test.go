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
}
