// Package tun 实现 TUN 引擎:用 gVisor netstack 在用户态终结 TCP/UDP,
// 把每条新连接的目标交给 Dialer,再双向 splice 字节。
package tun

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/getbx/bx/internal/route"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"
)

const (
	nicID          = 1
	tcpMaxInFlight = 2048
)

// Dialer 把一条连接的目标(Meta)落到实际出口,返回到出口的 net.Conn。
// 由上层 *dialer.Dialer 实现。
type Dialer interface {
	Dial(ctx context.Context, m route.Meta) (net.Conn, error)
}

// InitialDialer 可在 TCP 首包已知时用首包辅助恢复域名(如 TLS SNI/HTTP Host)。
type InitialDialer interface {
	DialWithInitial(ctx context.Context, m route.Meta, initial []byte) (net.Conn, error)
}

// DNSResponder 处理一条 DNS 查询(请求 → 应答字节)。用于 fake-IP。
type DNSResponder interface {
	Respond(query []byte) ([]byte, error)
}

// ConnCounter 记录连接与字节计数(由 stats.Counters 实现)。
type ConnCounter interface {
	ConnOpen()
	ConnClose()
	AddUp(n int64)
	AddDown(n int64)
}

// Engine 是 TUN 引擎:在 link 上跑 netstack,终结 TCP/UDP 并交给 Dialer。
type Engine struct {
	stack  *stack.Stack
	dialer Dialer
	dns    DNSResponder // 可空:非空时 UDP:53 由它就地应答(fake-IP)
	stats  ConnCounter  // 可空:活跃连接 + 上下行字节计数
}

// Option 配置 Engine。
type Option func(*Engine)

// WithDNS 让引擎就地处理 UDP:53 查询(fake-IP),不再转发到 Dialer。
func WithDNS(r DNSResponder) Option { return func(e *Engine) { e.dns = r } }

// WithStats 接上连接/字节计数器。
func WithStats(c ConnCounter) Option { return func(e *Engine) { e.stats = c } }

// New 在给定 link 端点上建引擎(测试用 channel/pipe,生产用 fdbased TUN)。
// 返回后即开始服务:netstack 收到新连接会回调 Dialer。
func New(link stack.LinkEndpoint, d Dialer, mtu uint32, opts ...Option) (*Engine, error) {
	s := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol, ipv6.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol},
	})
	e := &Engine{stack: s, dialer: d}
	for _, o := range opts {
		o(e)
	}

	if err := s.CreateNIC(nicID, link); err != nil {
		return nil, fmt.Errorf("create NIC: %v", err)
	}
	// TUN 看到的目标是任意 IP(含 fake-IP),NIC 自身无地址:
	// 混杂模式接收发往任意目标的包,spoofing 允许从任意源地址回包。
	if err := s.SetPromiscuousMode(nicID, true); err != nil {
		return nil, fmt.Errorf("set promiscuous: %v", err)
	}
	if err := s.SetSpoofing(nicID, true); err != nil {
		return nil, fmt.Errorf("set spoofing: %v", err)
	}
	s.SetRouteTable([]tcpip.Route{
		{Destination: header.IPv4EmptySubnet, NIC: nicID},
		{Destination: header.IPv6EmptySubnet, NIC: nicID},
	})

	tfwd := tcp.NewForwarder(s, 0, tcpMaxInFlight, e.handleTCP)
	s.SetTransportProtocolHandler(tcp.ProtocolNumber, tfwd.HandlePacket)

	ufwd := udp.NewForwarder(s, e.handleUDP)
	s.SetTransportProtocolHandler(udp.ProtocolNumber, ufwd.HandlePacket)

	return e, nil
}

// Close 拆掉协议栈,撤销 NIC。
func (e *Engine) Close() error {
	e.stack.Close()
	return nil
}

// handleTCP 接受一条被转发的 TCP 连接,交给 handleConn。
func (e *Engine) handleTCP(r *tcp.ForwarderRequest) {
	id := r.ID()
	var wq waiter.Queue
	ep, terr := r.CreateEndpoint(&wq)
	if terr != nil {
		r.Complete(true) // 发 RST
		return
	}
	r.Complete(false)
	conn := gonet.NewTCPConn(&wq, ep)
	go e.handleConn(conn, metaFromID(id, false))
}

// handleUDP 接受一条被转发的 UDP 流(按 5 元组),交给 handleConn。
func (e *Engine) handleUDP(r *udp.ForwarderRequest) bool {
	id := r.ID()
	var wq waiter.Queue
	ep, terr := r.CreateEndpoint(&wq)
	if terr != nil {
		return true // 已处理(丢弃)
	}
	conn := gonet.NewUDPConn(&wq, ep)
	go e.handleConn(conn, metaFromID(id, true))
	return true
}

// handleConn 把一条已终结的连接问 Dialer 拿到出口,再双向 splice。
// UDP:53 且配了 DNS 处理器时,就地应答(fake-IP),不转发。
func (e *Engine) handleConn(local net.Conn, m route.Meta) {
	if m.UDP && m.Port == 53 && e.dns != nil {
		e.serveDNS(local)
		return
	}
	initial := e.readInitial(local, m)
	var upstream net.Conn
	var err error
	if d, ok := e.dialer.(InitialDialer); ok {
		upstream, err = d.DialWithInitial(context.Background(), m, initial)
	} else {
		upstream, err = e.dialer.Dial(context.Background(), m)
	}
	if err != nil {
		local.Close()
		return
	}
	if e.stats != nil {
		e.stats.ConnOpen()
		defer e.stats.ConnClose()
	}
	e.relay(local, upstream, initial)
}

func (e *Engine) readInitial(conn net.Conn, m route.Meta) []byte {
	if m.UDP {
		return nil
	}
	_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil || n == 0 {
		return nil
	}
	return append([]byte(nil), buf[:n]...)
}

// serveDNS 在一条 UDP 流上循环处理 DNS 查询(请求→应答),空闲即关。
func (e *Engine) serveDNS(conn net.Conn) {
	defer conn.Close()
	buf := make([]byte, 4096) // EDNS0 可达 4096
	for {
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		n, err := conn.Read(buf)
		if err != nil {
			return
		}
		resp, err := e.dns.Respond(buf[:n])
		if err != nil {
			continue
		}
		if _, err := conn.Write(resp); err != nil {
			return
		}
	}
}

// relay 在 local↔upstream 间双向转发并计量字节:
// local→upstream 记为上行,upstream→local 记为下行。
// 任一方向读到 EOF 就半关闭对端的写,两个方向都结束后关闭两端。
func (e *Engine) relay(local, upstream net.Conn, initial []byte) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if len(initial) > 0 {
			if _, err := upstream.Write(initial); err != nil {
				return
			}
			if e.stats != nil {
				e.stats.AddUp(int64(len(initial)))
			}
		}
		n := copyOneWay(upstream, local, defaultIdleTimeout)
		if e.stats != nil {
			e.stats.AddUp(n)
		}
	}()
	go func() {
		defer wg.Done()
		n := copyOneWay(local, upstream, defaultIdleTimeout)
		if e.stats != nil {
			e.stats.AddDown(n)
		}
	}()
	wg.Wait()
	local.Close()
	upstream.Close()
}

// defaultIdleTimeout 是单向转发的空闲超时:超过该时长无数据则收尾,
// 防止挂死(half-open)连接永久泄漏 goroutine/fd。
const defaultIdleTimeout = 5 * time.Minute

// copyOneWay 把 src 转发到 dst,每次读写刷新空闲超时;返回转发字节数。
func copyOneWay(dst, src net.Conn, idle time.Duration) int64 {
	var total int64
	buf := make([]byte, 32*1024)
	for {
		_ = src.SetReadDeadline(time.Now().Add(idle))
		n, rerr := src.Read(buf)
		if n > 0 {
			_ = dst.SetWriteDeadline(time.Now().Add(idle))
			if _, werr := dst.Write(buf[:n]); werr != nil {
				break
			}
			total += int64(n)
		}
		if rerr != nil {
			break
		}
	}
	// 源读完/出错,给目标发 FIN(半关闭),让对端感知收尾;
	// 不支持 CloseWrite 的连接(如 net.Pipe)忽略。
	if cw, ok := dst.(interface{ CloseWrite() error }); ok {
		cw.CloseWrite()
	}
	return total
}

// metaFromID 把 netstack 的连接 ID 转成分流脑要的 Meta。
// 对 TUN 入站连接,LocalAddress/LocalPort 是程序要连的目标。
func metaFromID(id stack.TransportEndpointID, udp bool) route.Meta {
	return route.Meta{
		IP:   addrToNetip(id.LocalAddress),
		Port: id.LocalPort,
		UDP:  udp,
	}
}

// addrToNetip 把 tcpip.Address 转成 net/netip.Addr。
func addrToNetip(a tcpip.Address) netip.Addr {
	switch a.Len() {
	case 4:
		return netip.AddrFrom4(a.As4())
	case 16:
		return netip.AddrFrom16(a.As16())
	default:
		return netip.Addr{}
	}
}
