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

func TestUserProxyWinsWhenRulesConflict(t *testing.T) {
	r := &Router{
		UserDirect:   NewDomainSet([]string{"conflict.example"}),
		UserProxy:    NewDomainSet([]string{"conflict.example"}),
		UserDirectIP: mustSet([]string{"203.0.113.0/24"}),
		UserProxyIP:  mustSet([]string{"203.0.113.0/24"}),
	}
	if got := r.Decide(Meta{Domain: "conflict.example"}); got != Proxy {
		t.Fatalf("域名同时在 direct/proxy 时应保守走 Proxy, got %v", got)
	}
	if got := r.DecideIP(netip.MustParseAddr("203.0.113.9")); got != Proxy {
		t.Fatalf("IP 同时在 direct/proxy 时应保守走 Proxy, got %v", got)
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

// 透明代理永远不该把私网/docker/link-local 段隧道出去:它们应内建直连,
// 否则 bx up 后宿主机访问 docker 容器/内网会被甩给远端服务器而断网。
func TestPrivateRangesDirect(t *testing.T) {
	r := &Router{PrivateDirect: mustSet(DefaultPrivateCIDRs), GlobalProxy: true}
	for _, ip := range []string{
		"172.17.0.2",   // docker0 默认网桥
		"172.19.0.5",   // compose 网络(也是默认 tun-addr 所在段)
		"172.31.255.1", // docker 默认地址池上界
		"10.0.10.25",  // 内网(sandbox reward)
		"192.168.1.1",  // 家用 LAN
		"169.254.0.1",  // link-local
		"100.64.0.1",   // CGNAT
		"127.0.0.1",    // loopback
	} {
		if got := r.DecideIP(netip.MustParseAddr(ip)); got != Direct {
			t.Errorf("%s 应内建直连,got %v", ip, got)
		}
	}
	// fakeip 段不能被私网默认直连吃掉(它要进 DNS 反查),不在默认表内
	if got := r.DecideIP(netip.MustParseAddr("198.18.0.1")); got != Proxy {
		t.Errorf("fakeip 段不应被私网默认直连,got %v", got)
	}
}

// 用户显式 proxy 规则优先级高于私网内建直连,可强制把某私段走代理。
func TestUserProxyOverridesPrivateDirect(t *testing.T) {
	r := &Router{
		UserProxyIP:   mustSet([]string{"192.168.99.0/24"}),
		PrivateDirect: mustSet(DefaultPrivateCIDRs),
		GlobalProxy:   true,
	}
	if got := r.DecideIP(netip.MustParseAddr("192.168.99.5")); got != Proxy {
		t.Errorf("用户 proxy 规则应覆盖私网内建直连,got %v", got)
	}
	if got := r.DecideIP(netip.MustParseAddr("192.168.1.5")); got != Direct {
		t.Errorf("未被用户 proxy 覆盖的私段仍应直连,got %v", got)
	}
}
