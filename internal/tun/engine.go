// Package tun 实现 TUN 引擎:用 gVisor netstack 在用户态终结 TCP/UDP,
// 把每条新连接的目标交给 Dialer,再双向 splice 字节。
package tun

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
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

// 这些选项接口由指针接收者实现,故需取址;封成 helper 让 New 里一行登记。
func ptrSACK(b bool) *tcpip.TCPSACKEnabled {
	v := tcpip.TCPSACKEnabled(b)
	return &v
}

func ptrModerateRecvBuf(b bool) *tcpip.TCPModerateReceiveBufferOption {
	v := tcpip.TCPModerateReceiveBufferOption(b)
	return &v
}

func ptrTCPDelay(b bool) *tcpip.TCPDelayEnabled {
	v := tcpip.TCPDelayEnabled(b)
	return &v
}

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

// ByteAttributor 把转发的字节按**应用侧源端口**记账。可空。
//
// 用源端口而不是连接对象:端口是个小整数,记账端一次哈希 + 一次加法就能记完,
// 不必持有连接对象、也不必知道判定;代价是端口复用带来的近似(由记账端在见到
// 新连接时清账缓解)。**别照旧稿写「定长表、无锁 O(1)」** —— 归因键补上协议
// 维度之后定长表要开四张,实现(supervisor/apptraffic.go 的 addBytes)是
// map + 一把全局 mutex,未订阅时先由一次 atomic 读挡掉。
//
// udp 是**独立形参**,而不是让 tun 去拼一个 (端口,协议) 结构体:漏填一个
// 结构体字段没有编译错误(零值就是 false),而 TCP 与 UDP 的端口空间相互
// 独立,归错了在界面上完全看不出来 —— 只会看到一个应用名,看不到冲突。
// 附带好处是 internal/tun 不必 import internal/appattr。
//
// ConnClosed 报告一条连接结束。归因侧靠它维护「此刻还开着的连接」表 —— 那张表
// 让窗口在打开的那一刻就看得见**已经在跑**的长连接(会议媒体流、WebSocket、
// SSH),而不是只看得见打开之后新拨的连接。**它是那张表唯一的边界**:少了它,
// 表会随机器运行时间单调增长,而报告仍然完全正确、没有任何一处会报错。
type ByteAttributor interface {
	AddUp(srcPort uint16, udp bool, n int64)
	AddDown(srcPort uint16, udp bool, n int64)
	ConnClosed(srcPort uint16, udp bool)
}

// Engine 是 TUN 引擎:在 link 上跑 netstack,终结 TCP/UDP 并交给 Dialer。
type Engine struct {
	stack  *stack.Stack
	dialer Dialer
	dns    DNSResponder // 可空:非空时 UDP:53 由它就地应答(fake-IP)
	stats  ConnCounter  // 可空:活跃连接 + 上下行字节计数
	// bytes 可空:按 (源端口,协议) 的应用流量归因。与 stats 是两码事 ——
	// stats 是全局总量,这里是**谁的**流量。
	bytes ByteAttributor

	// idleTimeout 可空(零值走 defaultIdleTimeout);只有测试会设它 ——
	// 生产里等真实的 5 分钟没法测,而这条超时的语义正是缺陷所在。
	idleTimeout time.Duration
}

func (e *Engine) idle() time.Duration {
	if e.idleTimeout > 0 {
		return e.idleTimeout
	}
	return defaultIdleTimeout
}

// Option 配置 Engine。
type Option func(*Engine)

// WithDNS 让引擎就地处理 UDP:53 查询(fake-IP),不再转发到 Dialer。
func WithDNS(r DNSResponder) Option { return func(e *Engine) { e.dns = r } }

// WithStats 接上连接/字节计数器。
func WithStats(c ConnCounter) Option { return func(e *Engine) { e.stats = c } }

// WithByteAttribution 接上按源端口的字节归因。
func WithByteAttribution(b ByteAttributor) Option { return func(e *Engine) { e.bytes = b } }

// ByteAttributorOf 报告引擎当前接的字节归因器。
//
// **它是给组装根的测试开的窗口**:run.go 那一跳(dialer 与 engine 必须拿到
// 同一个 *AppTraffic 实例)住在 internal/supervisor,从那里看不见这个未导出
// 字段;没有这个窗口,那条不变量就只能靠读源码文本去守,而那类守卫在这个
// 仓库被攻破过八次。两个不同实例会让连接记录和字节账对不上,而两边各自看
// 起来都正常。
func ByteAttributorOf(e *Engine) ByteAttributor { return e.bytes }

// New 在给定 link 端点上建引擎(测试用 channel/pipe,生产用 fdbased TUN)。
// 返回后即开始服务:netstack 收到新连接会回调 Dialer。
func New(link stack.LinkEndpoint, d Dialer, mtu uint32, opts ...Option) (*Engine, error) {
	s := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol, ipv6.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol},
	})
	// TCP 吞吐调优:只影响 gVisor 协议栈的性能特性,不碰路由/killswitch/分流(零安全影响),
	// 与成熟用户态 TUN 代理(tun2socks/sing-box)一致。gVisor 这几项默认值偏保守:
	// - SACK 默认关 → 开启:丢包链路(蜂窝/跨境)选择性重传,吞吐大增、卡顿少。
	// - 接收缓冲自适应默认关 → 开启:接收窗口随 BDP 动态长到 4MB(MaxBufferSize),
	//   高带宽时延链路不被固定小窗口卡死(收发缓冲范围已是 1MB/4MB,无需再调)。
	// - Nagle 默认即关,显式设 false 防 gVisor 版本漂移:小包交互低延迟。
	for _, opt := range []tcpip.SettableTransportProtocolOption{
		ptrSACK(true), ptrModerateRecvBuf(true), ptrTCPDelay(false),
	} {
		if err := s.SetTransportProtocolOption(tcp.ProtocolNumber, opt); err != nil {
			return nil, fmt.Errorf("tcp 调优 %T: %v", opt, err)
		}
	}
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
	// **必须 defer 在拨号之前。** 判定(Dialer 内部的 recordApp)与活连接表的
	// 写入都发生在 Dial 里,而拨号失败(kill-switch Block、直连不通)一样会留下
	// 一条活连接记录;放到拨号成功之后才 defer,那些记录永远没人删 —— 隧道挂掉
	// 时被 Block 的连接恰恰是最多的。
	if e.bytes != nil {
		defer e.bytes.ConnClosed(m.SrcPort, m.UDP)
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
	e.relay(local, upstream, initial, m.SrcPort, m.UDP)
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
func (e *Engine) relay(local, upstream net.Conn, initial []byte, srcPort uint16, udp bool) {
	// **空闲是整条连接的属性,不是某一个方向的。** 两个方向共用这一份活跃时刻:
	// 任一方向有过流量,另一方向的超时就跟着续期。各计各的会让 SSE(纯服务端
	// 推送,客户端方向按构造永远静默)在第一个周期就被自己人 FIN 掉,WebSocket
	// 只要客户端不主动 ping 也一样 —— 而它防的那件事(两头都挂死的连接泄漏
	// goroutine/fd)一个字没变,由 relay_idle_test.go 两条测试各自钉住。
	activity := newRelayActivity()
	var wg sync.WaitGroup
	wg.Add(2)
	// **两个方向用同一套组装。** 上行那半原先只在 e.stats != nil 时才有
	// onWrite 闭包,而字节归因不该跟着 stats 的有无走:照抄那个形状会让
	// 「没接 stats 但有人订阅应用视图」时上行恒为 0,而下行是好的 ——
	// 界面上读起来就是「这个应用只在下载」,一句没人会去质疑的假话。
	up := e.writeHook(srcPort, udp, true)
	down := e.writeHook(srcPort, udp, false)
	go func() {
		defer wg.Done()
		if len(initial) > 0 {
			if _, err := upstream.Write(initial); err != nil {
				return
			}
			// 首包不经 copyOneWay,得自己记 —— 漏掉它,每条 TCP 连接的
			// TLS ClientHello / HTTP 请求头都不进账。
			if up != nil {
				up(int64(len(initial)))
			}
		}
		copyOneWay(upstream, local, e.idle(), activity, up)
	}()
	go func() {
		defer wg.Done()
		copyOneWay(local, upstream, e.idle(), activity, down)
	}()
	wg.Wait()
	local.Close()
	upstream.Close()
}

// writeHook 把「全局字节统计」与「按源端口的应用归因」合成一个 onWrite 闭包。
// 两者都没接时返回 nil,copyOneWay 便一次调用都不做。**说的是「都没接线」,
// 不是「没人在看」** —— 生产里 e.bytes 恒非 nil(wireAppAttribution 总会接),
// 「没人订阅」由 AppTraffic.addBytes 自己那一句 atomic 读挡掉。
func (e *Engine) writeHook(srcPort uint16, udp bool, up bool) func(int64) {
	stats, bytes := e.stats, e.bytes
	if stats == nil && bytes == nil {
		return nil
	}
	return func(n int64) {
		if stats != nil {
			if up {
				stats.AddUp(n)
			} else {
				stats.AddDown(n)
			}
		}
		if bytes != nil {
			if up {
				bytes.AddUp(srcPort, udp, n)
			} else {
				bytes.AddDown(srcPort, udp, n)
			}
		}
	}
}

// defaultIdleTimeout 是单向转发的空闲超时:超过该时长无数据则收尾,
// 防止挂死(half-open)连接永久泄漏 goroutine/fd。
const defaultIdleTimeout = 5 * time.Minute

// relayActivity 是一条连接上「最后一次有数据流动」的时刻,**两个方向共用一份**。
type relayActivity struct{ last atomic.Int64 }

func newRelayActivity() *relayActivity {
	a := &relayActivity{}
	a.mark()
	return a
}

func (a *relayActivity) mark() { a.last.Store(time.Now().UnixNano()) }

// deadline 是「若此刻之后再无任何流动,就该收尾」的时刻。读超时按它设,
// 于是另一个方向刚搬过数据时,这一边会自动续期。
func (a *relayActivity) deadline(idle time.Duration) time.Time {
	return time.Unix(0, a.last.Load()).Add(idle)
}

func (a *relayActivity) expired(idle time.Duration) bool {
	return !time.Now().Before(a.deadline(idle))
}

// isRelayTimeout 分辨「读超时」与真正的连接错误。**只有前者可以续期重来** ——
// 把 EOF/RST 也当成可续期的,连接就永远收不了尾,正好把空闲超时防的那个泄漏
// 变成必然。
func isRelayTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// copyOneWay 把 src 转发到 dst,每次读写刷新**整条连接**的活跃时刻;返回转发字节数。
// onWrite 非空时会在每次成功写入后调用,用于实时刷新流量统计。
func copyOneWay(dst, src net.Conn, idle time.Duration, activity *relayActivity, onWrite func(int64)) int64 {
	var total int64
	buf := make([]byte, 32*1024)
	for {
		_ = src.SetReadDeadline(activity.deadline(idle))
		n, rerr := src.Read(buf)
		if n > 0 {
			activity.mark()
			_ = dst.SetWriteDeadline(time.Now().Add(idle))
			written, werr := writeAll(dst, buf[:n], onWrite)
			total += written
			if werr != nil {
				break
			}
			activity.mark()
		}
		if rerr != nil {
			// 本方向读超时,但另一个方向刚搬过数据 —— 连接活着,续期重来。
			if isRelayTimeout(rerr) && !activity.expired(idle) {
				continue
			}
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

func writeAll(dst net.Conn, b []byte, onWrite func(int64)) (int64, error) {
	var total int64
	for len(b) > 0 {
		n, err := dst.Write(b)
		if n > 0 {
			written := int64(n)
			total += written
			if onWrite != nil {
				onWrite(written)
			}
			b = b[n:]
		}
		if err != nil {
			return total, err
		}
		if n == 0 {
			return total, net.ErrClosed
		}
	}
	return total, nil
}

// metaFromID 把 netstack 的连接 ID 转成分流脑要的 Meta。
// 对 TUN 入站连接,LocalAddress/LocalPort 是程序要连的目标。
func metaFromID(id stack.TransportEndpointID, udp bool) route.Meta {
	return route.Meta{
		IP:      addrToNetip(id.LocalAddress),
		Port:    id.LocalPort,
		UDP:     udp,
		SrcPort: id.RemotePort, // 应用侧端口:Local* 是目的地,Remote* 是发起方
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
