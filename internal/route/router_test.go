package route

import (
	"net/netip"
	"testing"
)

func mustSet(l []string) *CIDRSet { s, _ := NewCIDRSet(l); return s }

func newTestRouter() *Router {
	cn, _ := NewCIDRSet([]string{"1.2.0.0/16"}) // 假装 1.2.0.0/16 是中国
	return &Router{
		UserDirect:   NewDomainSet([]string{"*.internal.com"}),
		UserProxy:    NewDomainSet([]string{"*.openai.com"}),
		UserDirectIP: mustSet([]string{"10.0.0.0/8"}),
		ChinaDomain:  NewDomainSet([]string{"baidu.com"}),
		ChinaCIDR:    cn,
	}
}

func TestDecideGlobalProxy(t *testing.T) {
	r := newTestRouter()
	r.GlobalProxy = true
	tests := []struct {
		name string
		meta Meta
		want Decision
	}{
		// global 模式:china 列表/geoip 不再触发直连,一律走代理
		{"china 域名→代理", Meta{Domain: "x.baidu.com"}, Proxy},
		{"china 裸 IP→代理", Meta{IP: netip.MustParseAddr("1.2.3.4")}, Proxy},
		// 但用户显式 direct 规则仍生效(可保留例外)
		{"用户直连域名仍直连", Meta{Domain: "a.internal.com"}, Direct},
		{"用户直连网段仍直连", Meta{IP: netip.MustParseAddr("10.5.5.5")}, Direct},
	}
	for _, tc := range tests {
		if got := r.Decide(tc.meta); got != tc.want {
			t.Errorf("%s: Decide=%v want %v", tc.name, got, tc.want)
		}
	}
}

func TestDecide(t *testing.T) {
	r := newTestRouter()
	tests := []struct {
		name string
		meta Meta
		want Decision
	}{
		{"用户强制直连域名", Meta{Domain: "a.internal.com"}, Direct},
		{"用户强制代理域名", Meta{Domain: "api.openai.com"}, Proxy},
		{"china_domain 直连", Meta{Domain: "x.baidu.com"}, Direct},
		{"用户直连网段(裸IP)", Meta{IP: netip.MustParseAddr("10.5.5.5")}, Direct},
		{"中国IP裸连直连", Meta{IP: netip.MustParseAddr("1.2.3.4")}, Direct},
		{"外国IP裸连代理", Meta{IP: netip.MustParseAddr("8.8.8.8")}, Proxy},
	}
	for _, tc := range tests {
		if got := r.Decide(tc.meta); got != tc.want {
			t.Errorf("%s: Decide=%v want %v", tc.name, got, tc.want)
		}
	}
}

func TestDecideUnmatchedDomainProxy(t *testing.T) {
	// 未命中任何列表的域名默认走代理(避免被污染/CDN 误判直连泄漏真实 IP)。
	r := newTestRouter()
	if got := r.Decide(Meta{Domain: "unknown-foreign.com"}); got != Proxy {
		t.Fatalf("未命中域名应默认 Proxy,got %v", got)
	}
}

func TestDecideIP(t *testing.T) {
	r := newTestRouter()
	if r.DecideIP(netip.MustParseAddr("1.2.3.4")) != Direct {
		t.Fatal("china ip should be direct")
	}
	if r.DecideIP(netip.MustParseAddr("8.8.8.8")) != Proxy {
		t.Fatal("foreign ip should be proxy")
	}
}
