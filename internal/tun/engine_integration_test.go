package tun

import (
	"context"
	"io"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	bxdns "github.com/getbx/bx/internal/dns"
	"github.com/getbx/bx/internal/fakeip"
	"github.com/getbx/bx/internal/route"
	"golang.org/x/net/dns/dnsmessage"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/pipe"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
)

// captureDialer 记录每次 Dial 的 Meta,并把引擎侧连接的对端交给 test。
type captureDialer struct {
	mu    sync.Mutex
	metas []route.Meta
	peers chan net.Conn // test 侧拿到 upstream 的对端(用于断言字节转发)
}

func newCaptureDialer() *captureDialer {
	return &captureDialer{peers: make(chan net.Conn, 8)}
}

func (d *captureDialer) Dial(ctx context.Context, m route.Meta) (net.Conn, error) {
	d.mu.Lock()
	d.metas = append(d.metas, m)
	d.mu.Unlock()
	engineSide, testSide := net.Pipe()
	d.peers <- testSide
	return engineSide, nil
}

func (d *captureDialer) lastMeta(t *testing.T) route.Meta {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.metas) == 0 {
		t.Fatal("Dialer 未被调用")
	}
	return d.metas[len(d.metas)-1]
}

// newClientStack 起一个最简客户端协议栈,通过 link 把所有流量发往引擎侧。
func newClientStack(t *testing.T, link stack.LinkEndpoint, addr tcpip.Address) *stack.Stack {
	t.Helper()
	s := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol, ipv6.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol},
	})
	const nicID = 1
	if err := s.CreateNIC(nicID, link); err != nil {
		t.Fatalf("client CreateNIC: %v", err)
	}
	pa := tcpip.ProtocolAddress{Protocol: ipv4.ProtocolNumber, AddressWithPrefix: addr.WithPrefix()}
	if err := s.AddProtocolAddress(nicID, pa, stack.AddressProperties{}); err != nil {
		t.Fatalf("client AddProtocolAddress: %v", err)
	}
	s.SetRouteTable([]tcpip.Route{{Destination: header.IPv4EmptySubnet, NIC: nicID}})
	t.Cleanup(s.Close)
	return s
}

func TestEngine_TCP_DialerReceivesDestination(t *testing.T) {
	const mtu = 1500
	engineLink, clientLink := pipe.New("", "", mtu)

	dialer := newCaptureDialer()
	eng, err := New(engineLink, dialer, mtu)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer eng.Close()

	client := newClientStack(t, clientLink, tcpip.AddrFrom4([4]byte{10, 0, 0, 2}))

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	fa := tcpip.FullAddress{Addr: tcpip.AddrFrom4([4]byte{1, 2, 3, 4}), Port: 80}
	conn, err := gonet.DialContextTCP(ctx, client, fa, ipv4.ProtocolNumber)
	if err != nil {
		t.Fatalf("通过引擎拨号失败: %v", err)
	}
	defer conn.Close()

	select {
	case <-dialer.peers:
	case <-time.After(2 * time.Second):
		t.Fatal("引擎未在超时内调用 Dialer")
	}

	got := dialer.lastMeta(t)
	want := route.Meta{IP: netip.AddrFrom4([4]byte{1, 2, 3, 4}), Port: 80}
	if got != want {
		t.Fatalf("Meta = %+v, want %+v", got, want)
	}
}

func TestEngine_UDP_DialerReceivesDestination(t *testing.T) {
	const mtu = 1500
	engineLink, clientLink := pipe.New("", "", mtu)

	dialer := newCaptureDialer()
	eng, err := New(engineLink, dialer, mtu)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer eng.Close()

	client := newClientStack(t, clientLink, tcpip.AddrFrom4([4]byte{10, 0, 0, 2}))

	raddr := tcpip.FullAddress{Addr: tcpip.AddrFrom4([4]byte{1, 2, 3, 4}), Port: 53}
	conn, err := gonet.DialUDP(client, nil, &raddr, ipv4.ProtocolNumber)
	if err != nil {
		t.Fatalf("DialUDP: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("q")); err != nil {
		t.Fatalf("udp write: %v", err)
	}

	select {
	case <-dialer.peers:
	case <-time.After(2 * time.Second):
		t.Fatal("引擎未在超时内捕获 UDP 连接")
	}

	got := dialer.lastMeta(t)
	want := route.Meta{IP: netip.AddrFrom4([4]byte{1, 2, 3, 4}), Port: 53, UDP: true}
	if got != want {
		t.Fatalf("Meta = %+v, want %+v", got, want)
	}
}

func TestEngine_DNS_RespondsWithFakeIP(t *testing.T) {
	const mtu = 1500
	engineLink, clientLink := pipe.New("", "", mtu)

	pool, _ := fakeip.New("198.18.0.0/15")
	dialer := newCaptureDialer()
	eng, err := New(engineLink, dialer, mtu, WithDNS(bxdns.NewServer(pool, 1)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer eng.Close()

	client := newClientStack(t, clientLink, tcpip.AddrFrom4([4]byte{10, 0, 0, 2}))

	// 向任意 :53(这里 8.8.8.8)发查询;引擎应就地用 fake-IP 应答。
	raddr := tcpip.FullAddress{Addr: tcpip.AddrFrom4([4]byte{8, 8, 8, 8}), Port: 53}
	conn, err := gonet.DialUDP(client, nil, &raddr, ipv4.ProtocolNumber)
	if err != nil {
		t.Fatalf("DialUDP: %v", err)
	}
	defer conn.Close()

	qb := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: 0x1234, RecursionDesired: true})
	qb.StartQuestions()
	qb.Question(dnsmessage.Question{Name: dnsmessage.MustNewName("example.com."), Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET})
	query, _ := qb.Finish()
	if _, err := conn.Write(query); err != nil {
		t.Fatalf("写查询: %v", err)
	}

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1500)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("读应答: %v", err)
	}

	var p dnsmessage.Parser
	if _, err := p.Start(buf[:n]); err != nil {
		t.Fatalf("解析应答: %v", err)
	}
	p.SkipAllQuestions()
	if _, err := p.AnswerHeader(); err != nil {
		t.Fatalf("应答无 answer: %v", err)
	}
	ar, err := p.AResource()
	if err != nil {
		t.Fatalf("应答无 A 记录: %v", err)
	}
	ip := netip.AddrFrom4(ar.A)
	if dom, ok := pool.Domain(ip); !ok || dom != "example.com" {
		t.Errorf("fake IP %v 反查 = %q,%v; want example.com,true", ip, dom, ok)
	}

	// :53 不应走到 Dialer
	select {
	case <-dialer.peers:
		t.Error("DNS 查询不应触发 Dialer")
	default:
	}
}

func TestEngine_TCP_SplicesBytesBothWays(t *testing.T) {
	const mtu = 1500
	engineLink, clientLink := pipe.New("", "", mtu)

	dialer := newCaptureDialer()
	eng, err := New(engineLink, dialer, mtu)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer eng.Close()

	client := newClientStack(t, clientLink, tcpip.AddrFrom4([4]byte{10, 0, 0, 2}))

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	fa := tcpip.FullAddress{Addr: tcpip.AddrFrom4([4]byte{1, 2, 3, 4}), Port: 80}
	conn, err := gonet.DialContextTCP(ctx, client, fa, ipv4.ProtocolNumber)
	if err != nil {
		t.Fatalf("通过引擎拨号失败: %v", err)
	}
	defer conn.Close()

	var upstream net.Conn
	select {
	case upstream = <-dialer.peers:
	case <-time.After(2 * time.Second):
		t.Fatal("引擎未在超时内调用 Dialer")
	}
	defer upstream.Close()

	// app -> upstream
	go conn.Write([]byte("ping"))
	upstream.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 4)
	if _, err := io.ReadFull(upstream, buf); err != nil {
		t.Fatalf("upstream 读取失败: %v", err)
	}
	if string(buf) != "ping" {
		t.Fatalf("upstream 收到 %q, want %q", buf, "ping")
	}

	// upstream -> app
	go upstream.Write([]byte("pong"))
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	rbuf := make([]byte, 4)
	if _, err := io.ReadFull(conn, rbuf); err != nil {
		t.Fatalf("client 读取失败: %v", err)
	}
	if string(rbuf) != "pong" {
		t.Fatalf("client 收到 %q, want %q", rbuf, "pong")
	}
}
