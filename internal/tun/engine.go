// Package tun 实现 TUN 引擎:用 gVisor netstack 在用户态终结 TCP/UDP,
// 把每条新连接的目标交给 Dialer,再双向 splice 字节。
package tun

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/netip"
	"sync"

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

// Engine 是 TUN 引擎:在 link 上跑 netstack,终结 TCP/UDP 并交给 Dialer。
type Engine struct {
	stack  *stack.Stack
	dialer Dialer
}

// New 在给定 link 端点上建引擎(测试用 channel/pipe,生产用 fdbased TUN)。
// 返回后即开始服务:netstack 收到新连接会回调 Dialer。
func New(link stack.LinkEndpoint, d Dialer, mtu uint32) (*Engine, error) {
	s := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol, ipv6.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol},
	})
	e := &Engine{stack: s, dialer: d}

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
func (e *Engine) handleConn(local net.Conn, m route.Meta) {
	upstream, err := e.dialer.Dial(context.Background(), m)
	if err != nil {
		local.Close()
		return
	}
	relay(local, upstream)
}

// relay 在两条连接间双向转发,任一方向读到 EOF 就半关闭对端的写,
// 两个方向都结束后关闭两端。
func relay(a, b net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); copyOneWay(b, a) }()
	go func() { defer wg.Done(); copyOneWay(a, b) }()
	wg.Wait()
	a.Close()
	b.Close()
}

func copyOneWay(dst, src net.Conn) {
	io.Copy(dst, src)
	// 源读完了,给目标发 FIN(半关闭),让对端感知收尾;
	// 不支持 CloseWrite 的连接(如 net.Pipe)忽略。
	if cw, ok := dst.(interface{ CloseWrite() error }); ok {
		cw.CloseWrite()
	}
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
