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

// snapshot 返回目前记录到的全部 Meta 的副本 —— 拷贝而非直接返回底层切片,
// 避免测试断言期间与仍可能追加写入的 Dial 产生数据竞争。
func (d *captureDialer) snapshot() []route.Meta {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]route.Meta, len(d.metas))
	copy(out, d.metas)
	return out
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

// testClient 打包一次「引擎 + 客户端协议栈」经 pipe 链路互联的建栈,供多条
// 测试共用同一套连通方式(照抄自 TestEngine_TCP_DialerReceivesDestination /
// TestEngine_UDP_DialerReceivesDestination 原先各自重复的 setup)。
type testClient struct {
	eng    *Engine
	stack  *stack.Stack
	dialer *captureDialer
}

// newTestClient 起一对经 pipe 链路端点互联的引擎协议栈与客户端协议栈。
func newTestClient(t *testing.T, dialer *captureDialer) (*testClient, func()) {
	t.Helper()
	const mtu = 1500
	engineLink, clientLink := pipe.New("", "", mtu)

	eng, err := New(engineLink, dialer, mtu)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	clientStack := newClientStack(t, clientLink, tcpip.AddrFrom4([4]byte{10, 0, 0, 2}))

	c := &testClient{eng: eng, stack: clientStack, dialer: dialer}
	return c, func() { eng.Close() }
}

// connectTCP 以给定源端口经引擎向 dstIP:dstPort 发起 TCP 连接,并等待引擎侧
// Dialer 被调用(即该连接已被引擎捕获)。srcPort 为 0 时由协议栈自动分配临时端口。
func (c *testClient) connectTCP(t *testing.T, srcPort uint16, dstIP netip.Addr, dstPort uint16) *gonet.TCPConn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	laddr := tcpip.FullAddress{Port: srcPort}
	raddr := tcpip.FullAddress{Addr: tcpip.AddrFrom4(dstIP.As4()), Port: dstPort}
	conn, err := gonet.DialTCPWithBind(ctx, c.stack, laddr, raddr, ipv4.ProtocolNumber)
	if err != nil {
		t.Fatalf("通过引擎拨号失败: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	select {
	case <-c.dialer.peers:
	case <-time.After(2 * time.Second):
		t.Fatal("引擎未在超时内调用 Dialer")
	}
	return conn
}

// connectUDP 同 connectTCP,但走 UDP,并写一个字节让引擎捕获该流(UDP 无
// 握手,不主动发包引擎侧永远看不到这条连接)。
func (c *testClient) connectUDP(t *testing.T, srcPort uint16, dstIP netip.Addr, dstPort uint16) *gonet.UDPConn {
	t.Helper()
	laddr := tcpip.FullAddress{Port: srcPort}
	raddr := tcpip.FullAddress{Addr: tcpip.AddrFrom4(dstIP.As4()), Port: dstPort}
	conn, err := gonet.DialUDP(c.stack, &laddr, &raddr, ipv4.ProtocolNumber)
	if err != nil {
		t.Fatalf("DialUDP: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	if _, err := conn.Write([]byte("q")); err != nil {
		t.Fatalf("udp write: %v", err)
	}

	select {
	case <-c.dialer.peers:
	case <-time.After(2 * time.Second):
		t.Fatal("引擎未在超时内捕获 UDP 连接")
	}
	return conn
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
	// SrcPort 由协议栈自动分配(未显式绑定),故从实际建立的连接读回来比对,
	// 而不是硬编码一个值 —— 这条测试要守的是「目的地」,SrcPort 只需如实等于
	// 客户端真正用的那个临时端口(见 TestEngine_TCP_MetaCarriesApplicationSourcePort
	// 才是专门钉 SrcPort 语义的测试)。
	localPort := conn.LocalAddr().(*net.TCPAddr).Port
	want := route.Meta{IP: netip.AddrFrom4([4]byte{1, 2, 3, 4}), Port: 80, SrcPort: uint16(localPort)}
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
	// 同上一条 TCP 测试:SrcPort 由协议栈自动分配,从实际连接读回来比对。
	localPort := conn.LocalAddr().(*net.UDPAddr).Port
	want := route.Meta{IP: netip.AddrFrom4([4]byte{1, 2, 3, 4}), Port: 53, UDP: true, SrcPort: uint16(localPort)}
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

// TCP 调优必须真落到协议栈:SACK + 接收缓冲自适应启用、Nagle 关。
// 锁定它,防 gVisor 版本升级把默认/接口改了而我们悄悄退回保守默认(吞吐回归)。
func TestEngineTCPTuningApplied(t *testing.T) {
	const mtu = 1500
	engineLink, _ := pipe.New("", "", mtu)
	eng, err := New(engineLink, newCaptureDialer(), mtu)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer eng.Close()

	var sack tcpip.TCPSACKEnabled
	if err := eng.stack.TransportProtocolOption(tcp.ProtocolNumber, &sack); err != nil {
		t.Fatalf("读 SACK: %v", err)
	}
	if !bool(sack) {
		t.Error("SACK 应启用(丢包链路吞吐)")
	}
	var mod tcpip.TCPModerateReceiveBufferOption
	if err := eng.stack.TransportProtocolOption(tcp.ProtocolNumber, &mod); err != nil {
		t.Fatalf("读 moderate recv buffer: %v", err)
	}
	if !bool(mod) {
		t.Error("接收缓冲自适应应启用(高 BDP 吞吐)")
	}
	var delay tcpip.TCPDelayEnabled
	if err := eng.stack.TransportProtocolOption(tcp.ProtocolNumber, &delay); err != nil {
		t.Fatalf("读 delay: %v", err)
	}
	if bool(delay) {
		t.Error("Nagle 应关(交互低延迟)")
	}
}

// TUN 引擎看到的 id.RemotePort 必须就是应用侧 socket 的本地端口 —— 整个应用归因
// 靠它跟 macOS 的 pcblist(按 lport 索引)对上。这条关系此前只是推理:metaFromID
// 用 id.Local* 当目的地,于是 Remote* "应该"是应用侧。做到界面才发现对不上,代价
// 是整条链白写,所以第一步就钉死它。
func TestEngine_TCP_MetaCarriesApplicationSourcePort(t *testing.T) {
	const wantSrcPort = 51234

	dialer := newCaptureDialer()
	// 照抄 TestEngine_TCP_DialerReceivesDestination 的建栈与注入方式,
	// 唯一的区别是客户端源端口用 wantSrcPort 这个确定值。
	client, cleanup := newTestClient(t, dialer)
	defer cleanup()
	client.connectTCP(t, wantSrcPort, netip.MustParseAddr("198.18.0.7"), 443)

	metas := dialer.snapshot()
	if len(metas) != 1 {
		t.Fatalf("Dial 次数 = %d, want 1", len(metas))
	}
	if got := metas[0].SrcPort; got != wantSrcPort {
		t.Fatalf("Meta.SrcPort = %d, want %d —— join 键不成立,应用归因整条链无从对上", got, wantSrcPort)
	}
}

// UDP 那半同样要钉住 —— 腾讯会议的媒体流是 UDP,漏掉它等于漏掉这个功能
// 最初的用例。
func TestEngine_UDP_MetaCarriesApplicationSourcePort(t *testing.T) {
	const wantSrcPort = 51234

	dialer := newCaptureDialer()
	client, cleanup := newTestClient(t, dialer)
	defer cleanup()
	client.connectUDP(t, wantSrcPort, netip.MustParseAddr("198.18.0.7"), 443)

	metas := dialer.snapshot()
	if len(metas) != 1 {
		t.Fatalf("Dial 次数 = %d, want 1", len(metas))
	}
	if got := metas[0].SrcPort; got != wantSrcPort {
		t.Fatalf("Meta.SrcPort = %d, want %d —— join 键不成立,应用归因整条链无从对上", got, wantSrcPort)
	}
}
