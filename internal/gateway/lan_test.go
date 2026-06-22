package gateway

import (
	"reflect"
	"testing"
)

func TestSelectLANCIDRs(t *testing.T) {
	got := SelectLANCIDRs([]IfaceCIDR{
		{"br-lan", "192.168.8.1/24"},   // LAN → include (masked)
		{"br-guest", "10.0.5.1/24"},    // LAN → include
		{"wlan4", "10.0.6.225/23"},    // WAN uplink (not br-) → exclude
		{"eth0", "203.0.113.5/24"},     // WAN public → exclude
		{"br-lan", "192.168.8.1/24"},   // dup → once
		{"br-wan", "100.64.0.2/24"},    // CGNAT, not RFC1918 → exclude
		{"lo", "127.0.0.1/8"},          // loopback → exclude
	})
	want := []string{"192.168.8.0/24", "10.0.5.0/24"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestSelectLANCIDRsExcludesNonBridge(t *testing.T) {
	got := SelectLANCIDRs([]IfaceCIDR{{"eth0", "192.168.1.1/24"}})
	if len(got) != 0 {
		t.Fatalf("non-bridge must be excluded, got %v", got)
	}
}

func TestSelectLANCIDRsExcludesV6(t *testing.T) {
	got := SelectLANCIDRs([]IfaceCIDR{{"br-lan", "fd00::1/64"}})
	if len(got) != 0 {
		t.Fatalf("v6 must be excluded (router mode is v4-fakeip), got %v", got)
	}
}
