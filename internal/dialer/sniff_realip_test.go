package dialer

import (
	"context"
	"crypto/tls"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/getbx/bx/internal/fakeip"
	"github.com/getbx/bx/internal/route"
)

// clientHelloFor 造一段带 SNI 的 TLS ClientHello,给 DialWithInitial 当首包。
func clientHelloFor(t *testing.T, sni string) []byte {
	t.Helper()
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	go func() {
		c := tls.Client(client, &tls.Config{ServerName: sni, InsecureSkipVerify: true})
		_ = c.Handshake()
	}()
	_ = server.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 4096)
	n, err := server.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	return buf[:n]
}

// 真机 2026-09-05(公司工作站,bx global):`bx direct add 180.158.6.185` 之后
// `bx explain 180.158.6.185` 答 DIRECT,而 tailscaled 到那个 IP 的 TLS 连接照样
// 经隧道从 VPS 出去(家里的 derper 记到的源 IP 是 VPS;tcpdump 里 eno1 上一个
// 发往该 IP 的 TCP 包都没有)。机制:dialInner 从首包嗅出 SNI(brook.youdamaster.cc),
// 按域名判 —— 域名规则全部不中就默认走隧道,**IP 规则从头到尾没被问过**;而
// explain 没有首包,按 IP 判,自然说 DIRECT。嗅到的域名对一个**真 IP** 的连接只是
// 补充信息:域名说不出什么(默认)时,该由那个真 IP 说了算。
func TestSniffedSNIDoesNotOverrideAnIPDirectRuleOnARealIP(t *testing.T) {
	// 生产里 Fake 池永远在,嗅探只在它在时发生;不带池的测试根本走不到嗅探那一支。
	pool, err := fakeip.New("198.18.0.0/15")
	if err != nil {
		t.Fatal(err)
	}
	d, px, dr := newTestDialer(pool, fakeResolver{ip: netip.MustParseAddr("1.2.3.4")}, true, true)
	rt := d.router.Load()
	udi, _ := route.NewCIDRSet([]string{"180.158.6.185/32"})
	rt.UserDirectIP = udi
	d.SetRouter(rt)

	m := route.Meta{IP: netip.MustParseAddr("180.158.6.185"), Port: 33445}
	if got := d.Explain(m).Effective; got != EffectiveDirect {
		t.Fatalf("前置不成立:explain 对这个 IP 该答 direct,got %s", got)
	}
	hello := clientHelloFor(t, "brook.youdamaster.cc") // 不在任何域名规则里
	conn, err := d.DialWithInitial(context.Background(), m, hello)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	conn.Close()
	if px.lastAddr != "" || dr.lastAddr == "" {
		t.Fatalf("嗅到的 SNI 把一条 IP 直连规则压成了隧道:proxy=%q direct=%q(explain 说 direct,拨号却走隧道 —— 这正是真机上那个分歧)", px.lastAddr, dr.lastAddr)
	}
}

// 反向:域名规则**命中**时仍然由域名说了算(嗅到的 SNI 在 proxy 规则里 ⇒ 走隧道,
// 哪怕目的 IP 在 direct IP 规则里)。域名比 IP 更具体,这条优先级不变。
func TestSniffedSNIStillWinsWhenADomainRuleMatches(t *testing.T) {
	pool, err := fakeip.New("198.18.0.0/15")
	if err != nil {
		t.Fatal(err)
	}
	d, px, dr := newTestDialer(pool, fakeResolver{ip: netip.MustParseAddr("1.2.3.4")}, true, true)
	rt := d.router.Load()
	udi, _ := route.NewCIDRSet([]string{"180.158.6.185/32"})
	rt.UserDirectIP = udi
	rt.UserProxy = route.NewDomainSet([]string{"force-tunnel.example"})
	d.SetRouter(rt)

	m := route.Meta{IP: netip.MustParseAddr("180.158.6.185"), Port: 443}
	conn, err := d.DialWithInitial(context.Background(), m, clientHelloFor(t, "force-tunnel.example"))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	conn.Close()
	if px.lastAddr == "" || dr.lastAddr != "" {
		t.Fatalf("命中的 proxy 域名规则该压过 IP 直连规则:proxy=%q direct=%q", px.lastAddr, dr.lastAddr)
	}
}

// fake-IP 连接不受影响:反查出的域名不中任何规则时仍默认走隧道 —— 那个 IP 是假的,
// 按它判等于按 198.18/15 判,什么都判不出。
func TestFakeIPConnectionsStillDefaultToTunnelOnDomainMiss(t *testing.T) {
	pool, err := fakeip.New("198.18.0.0/15")
	if err != nil {
		t.Fatal(err)
	}
	fakeAddr := pool.Alloc("unlisted.example")
	d, px, dr := newTestDialer(pool, fakeResolver{ip: netip.MustParseAddr("1.2.3.4")}, true, true)
	rt := d.router.Load()
	udi, _ := route.NewCIDRSet([]string{"198.18.0.0/15"}) // 故意把假 IP 段写进 direct IP 规则
	rt.UserDirectIP = udi
	d.SetRouter(rt)

	conn, err := d.Dial(context.Background(), route.Meta{IP: fakeAddr, Port: 443})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	conn.Close()
	if px.lastAddr == "" || dr.lastAddr != "" {
		t.Fatalf("fake-IP 连接不该按假 IP 判 direct:proxy=%q direct=%q", px.lastAddr, dr.lastAddr)
	}
}

// IP 字面量不是域名:HTTP Host 里写的是 IP 时,嗅探不该把它当域名交出去。
func TestSniffIgnoresIPLiteralHosts(t *testing.T) {
	for _, req := range []string{
		"GET / HTTP/1.1\r\nHost: 180.158.6.185:33445\r\n\r\n",
		"GET / HTTP/1.1\r\nHost: [2001:db8::1]:443\r\n\r\n",
	} {
		if got := sniffDomain([]byte(req)); got != "" {
			t.Errorf("IP 字面量被当成域名嗅出来了: %q ← %q", got, req)
		}
	}
}
