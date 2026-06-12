package splitdns

import (
	"net/netip"
	"testing"
)

func TestSetAddContains(t *testing.T) {
	s := NewSet()
	ip := netip.MustParseAddr("10.0.13.45")
	if s.Contains(ip) {
		t.Fatal("空集不应命中")
	}
	s.Add(ip)
	if !s.Contains(ip) {
		t.Fatal("Add 后应命中")
	}
	if s.Contains(netip.MustParseAddr("8.8.8.8")) {
		t.Fatal("未加的 IP 不应命中")
	}
}
