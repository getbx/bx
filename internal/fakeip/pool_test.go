package fakeip

import (
	"net/netip"
	"testing"
)

// 池耗尽回绕时,复用的 IP 必须把旧域名的正向映射一并清除,
// 否则旧域名与新域名共享同一 fake IP,反查错乱导致误路由。
func TestPoolWraparoundEvictsOldMapping(t *testing.T) {
	p, _ := New("198.18.0.0/30") // 可用 .1 .2 .3
	a := p.Alloc("a.com")        // .1
	p.Alloc("b.com")             // .2
	p.Alloc("c.com")             // .3
	d := p.Alloc("d.com")        // 回绕复用 .1
	if a != d {
		t.Fatalf("回绕应复用首个 IP: a=%v d=%v", a, d)
	}
	if dom, _ := p.Domain(d); dom != "d.com" {
		t.Errorf("反查 %v = %q, want d.com", d, dom)
	}
	// 旧域名 a.com 的映射应已清除:再次分配应拿到与 d 不同的 IP。
	if again := p.Alloc("a.com"); again == d {
		t.Errorf("a.com 旧映射未清除,与 d.com 在 %v 冲突", d)
	}
}

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
