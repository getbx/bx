package dialer

import (
	"context"
	"net"
	"net/netip"
	"testing"

	"github.com/getbx/bx/internal/fakeip"
	"github.com/getbx/bx/internal/route"
	"github.com/getbx/bx/internal/splitdns"
)

// recordDialer 记录被请求拨号的地址,返回一个假连接。
type recordDialer struct{ lastAddr string }

func (r *recordDialer) DialContext(_ context.Context, _, addr string) (net.Conn, error) {
	r.lastAddr = addr
	c1, _ := net.Pipe()
	return c1, nil
}

type fakeResolver struct {
	ip  netip.Addr
	err error
}

func (f fakeResolver) Resolve(context.Context, string) (netip.Addr, error) { return f.ip, f.err }

func newTestDialer(fake *fakeip.Pool, res Resolver, healthy bool, ks bool) (*Dialer, *recordDialer, *recordDialer) {
	cn, _ := route.NewCIDRSet([]string{"1.2.0.0/16"})
	r := &route.Router{
		UserProxy:   route.NewDomainSet([]string{"*.openai.com"}),
		ChinaDomain: route.NewDomainSet([]string{"baidu.com"}),
		ChinaCIDR:   cn,
	}
	px, dr := &recordDialer{}, &recordDialer{}
	d := &Dialer{
		Fake: fake, Resolver: res, Proxy: px, Direct: dr,
		Healthy: func() bool { return healthy }, Killswitch: ks,
	}
	d.SetRouter(r)
	return d, px, dr
}

func TestDialProxyDomain(t *testing.T) {
	d, px, _ := newTestDialer(nil, fakeResolver{}, true, true)
	if _, err := d.Dial(context.Background(), route.Meta{Domain: "api.openai.com", Port: 443}); err != nil {
		t.Fatal(err)
	}
	if px.lastAddr != "api.openai.com:443" {
		t.Fatalf("代理应拨域名, got %q", px.lastAddr)
	}
}

func TestDialChinaDomainResolvesDirect(t *testing.T) {
	res := fakeResolver{ip: netip.MustParseAddr("1.2.3.4")}
	d, _, dr := newTestDialer(nil, res, true, true)
	if _, err := d.Dial(context.Background(), route.Meta{Domain: "x.baidu.com", Port: 80}); err != nil {
		t.Fatal(err)
	}
	if dr.lastAddr != "1.2.3.4:80" {
		t.Fatalf("直连应拨解析后的真实 IP, got %q", dr.lastAddr)
	}
}

func TestDialFakeIPRecoversDomain(t *testing.T) {
	pool, _ := fakeip.New("198.18.0.0/24")
	fip := pool.Alloc("api.openai.com")
	d, px, _ := newTestDialer(pool, fakeResolver{}, true, true)
	if _, err := d.Dial(context.Background(), route.Meta{IP: fip, Port: 443}); err != nil {
		t.Fatal(err)
	}
	if px.lastAddr != "api.openai.com:443" {
		t.Fatalf("应反查到域名再代理, got %q", px.lastAddr)
	}
}

func TestDialNeedResolveForeignGoesProxy(t *testing.T) {
	res := fakeResolver{ip: netip.MustParseAddr("8.8.8.8")}
	d, px, _ := newTestDialer(nil, res, true, true)
	if _, err := d.Dial(context.Background(), route.Meta{Domain: "unknown.com", Port: 443}); err != nil {
		t.Fatal(err)
	}
	if px.lastAddr != "unknown.com:443" {
		t.Fatalf("未命中且外国 IP 应代理(传域名), got %q", px.lastAddr)
	}
}

func TestDialRawChinaIPDirect(t *testing.T) {
	d, _, dr := newTestDialer(nil, fakeResolver{}, true, true)
	if _, err := d.Dial(context.Background(), route.Meta{IP: netip.MustParseAddr("1.2.3.4"), Port: 22}); err != nil {
		t.Fatal(err)
	}
	if dr.lastAddr != "1.2.3.4:22" {
		t.Fatalf("中国裸 IP 应直连, got %q", dr.lastAddr)
	}
}

func TestDialKillswitchBlocksWhenDown(t *testing.T) {
	d, _, _ := newTestDialer(nil, fakeResolver{}, false, true)
	if _, err := d.Dial(context.Background(), route.Meta{Domain: "api.openai.com", Port: 443}); err != ErrBlocked {
		t.Fatalf("隧道挂+kill-switch 应阻断, got %v", err)
	}
}

// 安全不变量:kill-switch 只阻断「代理」决策,直连域名在隧道挂时仍正常,
// 保证隧道故障期间国内服务不中断。
func TestDialKillswitchDirectDomainUnaffectedWhenDown(t *testing.T) {
	res := fakeResolver{ip: netip.MustParseAddr("1.2.3.4")}
	d, _, dr := newTestDialer(nil, res, false, true) // healthy=false, killswitch=true
	if _, err := d.Dial(context.Background(), route.Meta{Domain: "x.baidu.com", Port: 80}); err != nil {
		t.Fatalf("隧道挂时直连域名不应被阻断: %v", err)
	}
	if dr.lastAddr != "1.2.3.4:80" {
		t.Fatalf("应直连解析后的真实 IP, got %q", dr.lastAddr)
	}
}

// 安全不变量:裸中国 IP 在隧道挂+kill-switch 时仍直连。
func TestDialKillswitchRawChinaIPDirectWhenDown(t *testing.T) {
	d, _, dr := newTestDialer(nil, fakeResolver{}, false, true)
	if _, err := d.Dial(context.Background(), route.Meta{IP: netip.MustParseAddr("1.2.3.4"), Port: 22}); err != nil {
		t.Fatalf("隧道挂时裸中国 IP 不应被阻断: %v", err)
	}
	if dr.lastAddr != "1.2.3.4:22" {
		t.Fatalf("应直连, got %q", dr.lastAddr)
	}
}

func TestDialerHotSwapRouter(t *testing.T) {
	cn, _ := route.NewCIDRSet([]string{"1.2.0.0/16"})
	px, dr := &recordDialer{}, &recordDialer{}
	d := &Dialer{Proxy: px, Direct: dr, Healthy: func() bool { return true }}

	// 路由 A:8.8.8.8 非中国 → 代理
	d.SetRouter(&route.Router{ChinaCIDR: cn})
	if _, err := d.Dial(context.Background(), route.Meta{IP: netip.MustParseAddr("8.8.8.8"), Port: 443}); err != nil {
		t.Fatal(err)
	}
	if px.lastAddr != "8.8.8.8:443" {
		t.Fatalf("路由A应代理, got proxy=%q", px.lastAddr)
	}

	// 热切到路由 B:8.8.8.8 用户直连 → 直连
	udi, _ := route.NewCIDRSet([]string{"8.8.8.8/32"})
	d.SetRouter(&route.Router{UserDirectIP: udi})
	if _, err := d.Dial(context.Background(), route.Meta{IP: netip.MustParseAddr("8.8.8.8"), Port: 443}); err != nil {
		t.Fatal(err)
	}
	if dr.lastAddr != "8.8.8.8:443" {
		t.Fatalf("热切后应直连, got direct=%q", dr.lastAddr)
	}
}

func TestDialSplitDirectForcesDirect(t *testing.T) {
	set := splitdns.NewSet()
	ip := netip.MustParseAddr("10.0.13.45")
	set.Add(ip)

	direct, proxy := &recordDialer{}, &recordDialer{}
	d := &Dialer{Proxy: proxy, Direct: direct, SplitDirect: set}
	d.SetRouter(&route.Router{GlobalProxy: true}) // global:默认本会判 Proxy

	conn, err := d.Dial(context.Background(), route.Meta{IP: ip, Port: 443})
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	directUsed := direct.lastAddr != ""
	proxyUsed := proxy.lastAddr != ""
	if !directUsed || proxyUsed {
		t.Fatalf("split 命中应强制 Direct(direct.used=%v proxy.used=%v)", directUsed, proxyUsed)
	}
}

func TestDialNonSplitPublicGoesProxy(t *testing.T) {
	direct, proxy := &recordDialer{}, &recordDialer{}
	d := &Dialer{Proxy: proxy, Direct: direct, SplitDirect: splitdns.NewSet()}
	d.SetRouter(&route.Router{GlobalProxy: true})

	conn, err := d.Dial(context.Background(), route.Meta{IP: netip.MustParseAddr("1.1.1.1"), Port: 443})
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	directUsed := direct.lastAddr != ""
	proxyUsed := proxy.lastAddr != ""
	if !proxyUsed || directUsed {
		t.Fatalf("未命中 split 的公网 IP 应走 Proxy(direct.used=%v proxy.used=%v)", directUsed, proxyUsed)
	}
}
