package dialer

import (
	"context"
	"net"
	"net/netip"
	"sync"
	"testing"

	"github.com/getbx/bx/internal/fakeip"
	"github.com/getbx/bx/internal/route"
	"github.com/getbx/bx/internal/splitdns"
)

// recordDialer 记录被请求拨号的网络/地址,返回一个假连接。
type recordDialer struct {
	lastNetwork string
	lastAddr    string
}

func (r *recordDialer) DialContext(_ context.Context, network, addr string) (net.Conn, error) {
	r.lastNetwork = network
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
	d := &Dialer{Fake: fake, Resolver: res, Direct: dr, Killswitch: ks}
	d.SetTransport(&Transport{Proxy: px, Healthy: func() bool { return healthy }})
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

func TestDialBlocksUDPBeforeSocks(t *testing.T) {
	d, px, dr := newTestDialer(nil, fakeResolver{}, true, true)
	if _, err := d.Dial(context.Background(), route.Meta{IP: netip.MustParseAddr("198.18.0.93"), Port: 443, UDP: true}); err != ErrBlocked {
		t.Fatalf("UDP 应快速阻断, got %v", err)
	}
	if px.lastAddr != "" || dr.lastAddr != "" {
		t.Fatalf("UDP 不应触达 socks/direct, proxy=%q direct=%q", px.lastAddr, dr.lastAddr)
	}
}

func TestDialDirectRealtimeUDPUsesDirect(t *testing.T) {
	d, px, dr := newTestDialer(nil, fakeResolver{}, true, true)
	d.UDPMode = "direct-realtime"
	if _, err := d.Dial(context.Background(), route.Meta{IP: netip.MustParseAddr("198.18.0.93"), Port: 3478, UDP: true}); err != nil {
		t.Fatalf("direct-realtime UDP should direct dial: %v", err)
	}
	if dr.lastAddr != "198.18.0.93:3478" {
		t.Fatalf("UDP direct target = %q, want 198.18.0.93:3478", dr.lastAddr)
	}
	if px.lastAddr != "" {
		t.Fatalf("UDP direct-realtime should not touch proxy, got %q", px.lastAddr)
	}
}

func TestDialDirectRealtimeUDPResolvesFakeIP(t *testing.T) {
	pool, _ := fakeip.New("198.18.0.0/15")
	fip := pool.Alloc("stun.l.google.com")
	d, _, dr := newTestDialer(pool, fakeResolver{ip: netip.MustParseAddr("74.125.250.129")}, true, true)
	d.UDPMode = "direct-realtime"
	if _, err := d.Dial(context.Background(), route.Meta{IP: fip, Port: 19302, UDP: true}); err != nil {
		t.Fatalf("direct-realtime fake UDP should resolve and direct dial: %v", err)
	}
	if dr.lastAddr != "74.125.250.129:19302" {
		t.Fatalf("UDP fake-IP direct target = %q, want 74.125.250.129:19302", dr.lastAddr)
	}
}

// 安全不变量:direct-realtime 的境外 UDP 直连(牺牲匿名换低延迟)只在代理正常工作时被接受。
// 隧道挂 + kill-switch 时,不再直连泄漏真实 IP,而是 fail-closed 阻断。
func TestDialDirectRealtimeUDPKillswitchBlocksWhenDown(t *testing.T) {
	d, px, dr := newTestDialer(nil, fakeResolver{}, false, true) // healthy=false, killswitch=true
	d.UDPMode = "direct-realtime"
	if _, err := d.Dial(context.Background(), route.Meta{IP: netip.MustParseAddr("198.18.0.93"), Port: 3478, UDP: true}); err != ErrBlocked {
		t.Fatalf("隧道挂+kill-switch 时 direct-realtime 应 fail-closed,got %v", err)
	}
	if dr.lastAddr != "" || px.lastAddr != "" {
		t.Fatalf("阻断时不应触达 direct/proxy,direct=%q proxy=%q", dr.lastAddr, px.lastAddr)
	}
}

func TestDialProxyUDPUsesProxy(t *testing.T) {
	d, px, dr := newTestDialer(nil, fakeResolver{}, true, true)
	d.UDPMode = "proxy"
	if _, err := d.Dial(context.Background(), route.Meta{IP: netip.MustParseAddr("8.8.8.8"), Port: 3478, UDP: true}); err != nil {
		t.Fatalf("proxy UDP should dial proxy: %v", err)
	}
	if px.lastNetwork != "udp" || px.lastAddr != "8.8.8.8:3478" {
		t.Fatalf("UDP proxy dial = %s %q, want udp 8.8.8.8:3478", px.lastNetwork, px.lastAddr)
	}
	if dr.lastAddr != "" {
		t.Fatalf("UDP proxy should not touch direct, got %q", dr.lastAddr)
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
	d := &Dialer{Direct: dr}
	d.SetTransport(&Transport{Proxy: px, Healthy: func() bool { return true }})

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
	d := &Dialer{Direct: direct, SplitDirect: set}
	d.SetTransport(&Transport{Proxy: proxy})
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
	d := &Dialer{Direct: direct, SplitDirect: splitdns.NewSet()}
	d.SetTransport(&Transport{Proxy: proxy})
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

func TestSetTransportSwaps(t *testing.T) {
	pxA, pxB, dr := &recordDialer{}, &recordDialer{}, &recordDialer{}
	d := &Dialer{Direct: dr}
	d.SetTransport(&Transport{Proxy: pxA, Healthy: func() bool { return true }})
	d.SetRouter(&route.Router{GlobalProxy: true}) // 公网默认走 proxy

	m := route.Meta{IP: netip.MustParseAddr("8.8.8.8"), Port: 443}
	if _, err := d.Dial(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if pxA.lastAddr != "8.8.8.8:443" || pxB.lastAddr != "" {
		t.Fatalf("换前应命中 pxA: A=%q B=%q", pxA.lastAddr, pxB.lastAddr)
	}

	d.SetTransport(&Transport{Proxy: pxB, Healthy: func() bool { return true }})
	if _, err := d.Dial(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if pxB.lastAddr != "8.8.8.8:443" {
		t.Fatalf("换后应命中 pxB, got %q", pxB.lastAddr)
	}
}

func TestDialSetTransportRace(t *testing.T) {
	dr := &recordDialer{}
	d := &Dialer{Direct: dr}
	d.SetTransport(&Transport{Proxy: &recordDialer{}, Healthy: func() bool { return true }})
	d.SetRouter(&route.Router{GlobalProxy: true})

	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				d.SetTransport(&Transport{Proxy: &recordDialer{}, Healthy: func() bool { return true }})
			}
		}
	}()
	m := route.Meta{IP: netip.MustParseAddr("8.8.8.8"), Port: 443}
	for i := 0; i < 200; i++ {
		if c, err := d.Dial(context.Background(), m); err == nil {
			c.Close()
		}
	}
	close(stop)
	wg.Wait()
}
