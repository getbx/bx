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

// **白名单只收带 via 的那几条,不依赖 config 层先挡一道。**
//
// config.Parse 已经拒绝「有 cidr 没 via」,所以这条在今天不可达 —— 但判据不该
// 靠另一层的校验活着:两层各自成立,才是「白名单」这个承诺的真正保证。
func TestBuildRouterOnlyPutsViaRulesIntoTheEgressWhitelist(t *testing.T) {
	cfg := &config.Config{Rules: []config.Rule{
		{Direct: []string{"10.0.0.0/8"}},                // 普通直连规则,不该进出口
		{Proxy: []string{"203.0.113.0/24"}},             // 同上
		{CIDR: []string{"192.168.7.0/24"}},              // 漏了 via(config 层会拒,这里也不许收)
		{Via: "office", CIDR: []string{"10.84.0.0/16"}}, // 只有这一条
	}}
	r, err := BuildRouter(cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.EgressFor(netip.MustParseAddr("10.84.3.239")); got != "office" {
		t.Fatalf("点名的那条没进白名单:%q", got)
	}
	for _, addr := range []string{"10.0.0.5", "203.0.113.9", "192.168.7.9"} {
		if got := r.EgressFor(netip.MustParseAddr(addr)); got != "" {
			t.Errorf("%s 被塞进了出口白名单(归给 %q)—— 白名单只收带 via 的那几条", addr, got)
		}
	}
}
