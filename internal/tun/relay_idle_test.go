package tun

import (
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// tcpPair 给一对真 TCP 连接。**不能用 net.Pipe** —— 这个缺陷的表现形式是
// `CloseWrite()`(半关闭)提前发出去,而 net.Pipe 根本不实现 CloseWrite,
// relay 里那句类型断言会静默跳过,于是缺陷在 pipe 上根本复现不出来。
func tcpPair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	type accepted struct {
		conn net.Conn
		err  error
	}
	accepts := make(chan accepted, 1)
	go func() {
		conn, err := listener.Accept()
		accepts <- accepted{conn, err}
	}()

	dialed, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	got := <-accepts
	if got.err != nil {
		t.Fatal(got.err)
	}
	t.Cleanup(func() { dialed.Close(); got.conn.Close() })
	return dialed, got.conn
}

// SSE / WebSocket 的形状:服务端一直在推,客户端一个字节都不发。
//
// 缺陷(2026-08-19,用户报「websocket 不稳」):两个方向各自独立计空闲超时,
// 只由**本方向**的流量刷新。客户端安静满 5 分钟,读 local 那半边就超时 break,
// 顺手 `CloseWrite()` 给上游发 FIN —— **哪怕服务端这 5 分钟一直在推数据**。
// SSE 按构造必死(它本来就没有客户端→服务端方向),WebSocket 只要客户端不主动
// ping 就同样死。这条路径一行日志都不打,所以在 sing-box 的错误统计里完全看不见。
//
// 空闲应该是**整条连接**的属性,不是某一个方向的。
func TestRelayDoesNotHalfCloseWhileTheOtherDirectionIsActive(t *testing.T) {
	appSide, localSide := tcpPair(t)
	upstreamSide, serverSide := tcpPair(t)

	const idle = 150 * time.Millisecond
	engine := &Engine{idleTimeout: idle}

	relayDone := make(chan struct{})
	go func() {
		defer close(relayDone)
		engine.relay(localSide, upstreamSide, nil, 0, false)
	}()

	// 服务端探测:relay 何时给我们发 FIN(读到 EOF)。
	var mu sync.Mutex
	var sawEOF bool
	go func() {
		buf := make([]byte, 256)
		for {
			n, err := serverSide.Read(buf)
			if err != nil {
				mu.Lock()
				sawEOF = errors.Is(err, io.EOF)
				mu.Unlock()
				return
			}
			_ = n
		}
	}()

	// 服务端持续推送,横跨好几个空闲周期;客户端只读不写。
	push := time.NewTicker(idle / 5)
	defer push.Stop()
	deadline := time.After(idle * 5)

	received := make(chan int, 64)
	go func() {
		buf := make([]byte, 256)
		for {
			n, err := appSide.Read(buf)
			if err != nil {
				close(received)
				return
			}
			received <- n
		}
	}()

pump:
	for {
		select {
		case <-push.C:
			if _, err := serverSide.Write([]byte("event: ping\n\n")); err != nil {
				break pump
			}
		case <-deadline:
			break pump
		}
	}

	mu.Lock()
	closedEarly := sawEOF
	mu.Unlock()
	if closedEarly {
		t.Fatal("relay half-closed the upstream while the server was still pushing: every SSE stream and every WebSocket with a quiet client dies on the idle timer")
	}

	select {
	case <-relayDone:
		t.Fatal("relay tore the connection down while it was actively carrying data")
	default:
	}
}

// 反向守卫:空闲超时的**本意**(防挂死连接泄漏 goroutine/fd)不许被这次修复抹掉。
// 两个方向都静默满一个周期,relay 必须收尾。
func TestRelayStillTearsDownWhenBothDirectionsAreSilent(t *testing.T) {
	_, localSide := tcpPair(t)
	upstreamSide, _ := tcpPair(t)

	const idle = 150 * time.Millisecond
	engine := &Engine{idleTimeout: idle}

	done := make(chan struct{})
	go func() {
		defer close(done)
		engine.relay(localSide, upstreamSide, nil, 0, false)
	}()

	select {
	case <-done:
	case <-time.After(idle * 20):
		t.Fatal("both directions silent but relay never returned: hung connections leak goroutines and fds")
	}
}
