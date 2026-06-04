package supervisor

import (
	"net/netip"
	"testing"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/route"
)

func TestBuildRouter_SplitsDomainsAndCIDRs(t *testing.T) {
	cfg := &config.Config{
		Rules: []config.Rule{
			{Direct: []string{"*.internal.com", "10.0.0.0/8"}},
			{Proxy: []string{"*.openai.com", "1.2.3.0/24"}},
		},
	}
	chinaDomain := []string{"baidu.com"}
	chinaCIDR := []string{"114.114.114.0/24"}

	r, err := BuildRouter(cfg, chinaDomain, chinaCIDR)
	if err != nil {
		t.Fatalf("BuildRouter: %v", err)
	}

	// 域名规则进 DomainSet,CIDR 规则进 CIDRSet
	if !r.UserDirect.Match("x.internal.com") {
		t.Error("UserDirect 应命中 *.internal.com")
	}
	if !r.UserProxy.Match("api.openai.com") {
		t.Error("UserProxy 应命中 *.openai.com")
	}
	if !r.UserDirectIP.Contains(netip.MustParseAddr("10.1.2.3")) {
		t.Error("UserDirectIP 应含 10.0.0.0/8")
	}
	if !r.UserProxyIP.Contains(netip.MustParseAddr("1.2.3.4")) {
		t.Error("UserProxyIP 应含 1.2.3.0/24")
	}
	if !r.ChinaDomain.Match("baidu.com") {
		t.Error("ChinaDomain 应含 baidu.com")
	}
	if !r.ChinaCIDR.Contains(netip.MustParseAddr("114.114.114.5")) {
		t.Error("ChinaCIDR 应含 114.114.114.0/24")
	}
}

func TestBuildRouter_BareIPInRules(t *testing.T) {
	cfg := &config.Config{Rules: []config.Rule{{Direct: []string{"8.8.8.8"}}}}
	r, err := BuildRouter(cfg, nil, nil)
	if err != nil {
		t.Fatalf("BuildRouter: %v", err)
	}
	if !r.UserDirectIP.Contains(netip.MustParseAddr("8.8.8.8")) {
		t.Error("规则里的裸 IP 8.8.8.8 应进 UserDirectIP(不应被静默丢弃)")
	}
}

func TestBuildRouter_GeoIPSplit(t *testing.T) {
	cfg := &config.Config{}
	r, err := BuildRouter(cfg, nil, []string{"114.114.114.0/24"})
	if err != nil {
		t.Fatalf("BuildRouter: %v", err)
	}

	// 中国 IP 直连,境外 IP 走代理(裸 IP,无域名)
	cn := route.Meta{IP: netip.MustParseAddr("114.114.114.5"), Port: 443}
	if got := r.Decide(cn); got != route.Direct {
		t.Errorf("中国 IP 应 Direct,得到 %v", got)
	}
	foreign := route.Meta{IP: netip.MustParseAddr("8.8.8.8"), Port: 443}
	if got := r.Decide(foreign); got != route.Proxy {
		t.Errorf("境外 IP 应 Proxy,得到 %v", got)
	}
}
