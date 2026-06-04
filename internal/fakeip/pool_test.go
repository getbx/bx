package fakeip

import (
	"net/netip"
	"testing"
)

func TestPoolAllocAndLookup(t *testing.T) {
	p, err := New("198.18.0.0/24")
	if err != nil {
		t.Fatal(err)
	}
	ip1 := p.Alloc("google.com")
	ip2 := p.Alloc("baidu.com")
	if ip1 == ip2 {
		t.Fatal("different domains must get different IPs")
	}
	if !netip.MustParsePrefix("198.18.0.0/24").Contains(ip1) {
		t.Fatalf("ip %v not in pool range", ip1)
	}
	if p.Alloc("google.com") != ip1 {
		t.Fatal("same domain must be stable")
	}
	if d, ok := p.Domain(ip1); !ok || d != "google.com" {
		t.Fatalf("reverse lookup failed: %q %v", d, ok)
	}
	if _, ok := p.Domain(netip.MustParseAddr("9.9.9.9")); ok {
		t.Fatal("unknown ip should not resolve")
	}
}
