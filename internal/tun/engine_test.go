package tun

import (
	"net"
	"net/netip"
	"os"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/getbx/bx/internal/route"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

// 挂死(无数据)连接应被空闲超时收尾,copyOneWay 返回,避免 goroutine/fd 泄漏。
func TestCopyOneWay_IdleTimeout(t *testing.T) {
	src, _ := net.Pipe() // 对端永不写,src.Read 将一直阻塞直到空闲超时
	dst, _ := net.Pipe()
	done := make(chan int64, 1)
	go func() { done <- copyOneWay(dst, src, 80*time.Millisecond, newRelayActivity(), nil) }()

	select {
	case n := <-done:
		if n != 0 {
			t.Errorf("空闲连接应转发 0 字节, got %d", n)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("空闲超时未触发:copyOneWay 未及时返回(连接会泄漏)")
	}
}

// **一个「看起来像超时」但**不是**我们那个 deadline 的错误,不许被续期。**
//
// `net.Error.Timeout()` 比「我设的 deadline 到了」宽:`syscall.ETIMEDOUT`
// (TCP 重传耗尽 —— 连接真死了)与 `context.DeadlineExceeded` 都满足它。把真死的
// upstream 判成可续期之后,循环只剩「下一次读恰好返回 EOF/RST」这一条出路,而那是
// 一条没写在任何地方的内核假设。全分支复审量过代价:**300ms 内 757 万次 Read,
// 一个核跑满,不打一行日志、界面完全正常** —— 这条失效唯一的信号是风扇。
//
// 这里让 src 每次都**立刻**返回一个 Timeout()==true 的错误(远早于 deadline),
// 同时让另一个方向持续保持活跃(activity 一直在被 mark),于是「整条连接空闲」
// 那半永远为假 —— 只有「这真的是我那个 deadline 吗」这一半救得了它。
func TestCopyOneWay_DoesNotSpinOnATimeoutThatIsNotOurDeadline(t *testing.T) {
	src := &instantTimeoutConn{}
	dst, _ := net.Pipe()
	defer dst.Close()
	activity := newRelayActivity()
	stop := make(chan struct{})
	defer close(stop)
	go func() { // 另一个方向一直在搬数据
		for {
			select {
			case <-stop:
				return
			default:
				activity.mark()
				time.Sleep(time.Millisecond)
			}
		}
	}()

	done := make(chan int64, 1)
	go func() { done <- copyOneWay(dst, src, time.Hour, activity, nil) }()
	select {
	case <-done:
		if got := src.reads.Load(); got > 100 {
			t.Errorf("收尾前空转了 %d 次读 —— 判据认的是「像超时」而不是「我的 deadline 到了」", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("一个真死的连接被当成可续期,copyOneWay 空转不返回(已读 %d 次)", src.reads.Load())
	}
}

// instantTimeoutConn 的 Read 立刻返回一个 Timeout()==true 的错误 —— 模拟
// syscall.ETIMEDOUT 经 net.OpError 冒上来的形状,而不是我们自己的 deadline。
type instantTimeoutConn struct {
	net.Conn
	reads atomic.Int64
}

func (c *instantTimeoutConn) Read([]byte) (int, error) {
	c.reads.Add(1)
	return 0, &net.OpError{Op: "read", Err: os.NewSyscallError("read", syscall.ETIMEDOUT)}
}
func (c *instantTimeoutConn) SetReadDeadline(time.Time) error { return nil }
func (c *instantTimeoutConn) Close() error                    { return nil }

func TestCopyOneWay_ReportsBytesAsWritten(t *testing.T) {
	srcReader, srcWriter := net.Pipe()
	dstReader, dstWriter := net.Pipe()
	defer srcReader.Close()
	defer srcWriter.Close()
	defer dstReader.Close()
	defer dstWriter.Close()

	reported := make(chan int64, 1)
	done := make(chan int64, 1)
	go func() {
		done <- copyOneWay(dstWriter, srcReader, time.Second, newRelayActivity(), func(n int64) {
			reported <- n
		})
	}()

	go func() {
		_, _ = srcWriter.Write([]byte("pong"))
		_ = srcWriter.Close()
	}()

	buf := make([]byte, 4)
	_ = dstReader.SetReadDeadline(time.Now().Add(time.Second))
	if n, err := dstReader.Read(buf); err != nil || n != 4 || string(buf) != "pong" {
		t.Fatalf("dst read n=%d err=%v buf=%q", n, err, buf[:n])
	}
	select {
	case n := <-reported:
		if n != 4 {
			t.Fatalf("reported = %d, want 4", n)
		}
	case <-time.After(time.Second):
		t.Fatal("write callback was not called before copy finished")
	}
	if n := <-done; n != 4 {
		t.Fatalf("copyOneWay returned %d, want 4", n)
	}
}

// TestCopyOneWay_StreamsIncrementally 钉死 AI 流式依赖的关键性质:中继把带间隔的多 chunk
// 增量透传(逐 chunk 读即写、不缓冲/不合并),且间隔内的 chunk 全部存活(逐字节空闲刷新成立)。
// net.Pipe 同步无缓冲:一次 Write 配一次 Read 且 Write 阻塞到读完,故每个 chunk 1:1 映射到
// dst 的一次 Read;若中继缓冲/合并则此测失败。
func TestCopyOneWay_StreamsIncrementally(t *testing.T) {
	srcReader, srcWriter := net.Pipe()
	dstReader, dstWriter := net.Pipe()
	defer srcReader.Close()
	defer srcWriter.Close()
	defer dstReader.Close()
	defer dstWriter.Close()

	go copyOneWay(dstWriter, srcReader, 5*time.Second, newRelayActivity(), nil)

	chunks := []string{"data: tok1\n\n", "data: tok2\n\n", "data: tok3\n\n"}
	go func() {
		for _, c := range chunks {
			_, _ = srcWriter.Write([]byte(c))
			time.Sleep(20 * time.Millisecond) // “token”之间的间隔
		}
		_ = srcWriter.Close()
	}()

	buf := make([]byte, 4096)
	for i, want := range chunks {
		_ = dstReader.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, err := dstReader.Read(buf)
		if err != nil {
			t.Fatalf("chunk %d 读失败 err=%v(未增量透传?)", i, err)
		}
		if got := string(buf[:n]); got != want {
			t.Fatalf("chunk %d got %q want %q(被缓冲/合并?)", i, got, want)
		}
	}
}

// metaFromID 把 netstack 的连接 ID 转成分流脑要的 Meta:
// TUN 入站连接里,LocalAddress/LocalPort 就是程序要连的目标。
func TestMetaFromID_TCP(t *testing.T) {
	id := stack.TransportEndpointID{
		LocalAddress:  tcpip.AddrFrom4([4]byte{1, 2, 3, 4}),
		LocalPort:     443,
		RemoteAddress: tcpip.AddrFrom4([4]byte{10, 0, 0, 2}),
		RemotePort:    51000,
	}

	got := metaFromID(id, false)

	// SrcPort 取自 id.RemotePort(应用侧端口,Local* 才是目的地)——见
	// internal/tun/engine_integration_test.go 里的
	// TestEngine_TCP_MetaCarriesApplicationSourcePort,那条测试用真 netstack
	// 钉住这条关系,这里只是同一份实现在单元测试层面的直接验证。
	want := route.Meta{IP: netip.AddrFrom4([4]byte{1, 2, 3, 4}), Port: 443, UDP: false, SrcPort: 51000}
	if got != want {
		t.Fatalf("metaFromID = %+v, want %+v", got, want)
	}
}

func TestMetaFromID_UDP_IPv6(t *testing.T) {
	raw := [16]byte{0x20, 0x01, 0x0d, 0xb8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x35}
	id := stack.TransportEndpointID{
		LocalAddress: tcpip.AddrFrom16(raw),
		LocalPort:    53,
	}

	got := metaFromID(id, true)

	if !got.UDP {
		t.Errorf("UDP = false, want true")
	}
	if got.Port != 53 {
		t.Errorf("Port = %d, want 53", got.Port)
	}
	wantIP := netip.AddrFrom16(raw)
	if got.IP != wantIP {
		t.Errorf("IP = %v, want %v", got.IP, wantIP)
	}
}
