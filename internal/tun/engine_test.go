package tun

import (
	"net"
	"net/netip"
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
	go func() { done <- copyOneWay(dst, src, 80*time.Millisecond, nil) }()

	select {
	case n := <-done:
		if n != 0 {
			t.Errorf("空闲连接应转发 0 字节, got %d", n)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("空闲超时未触发:copyOneWay 未及时返回(连接会泄漏)")
	}
}

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
		done <- copyOneWay(dstWriter, srcReader, time.Second, func(n int64) {
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

	want := route.Meta{IP: netip.AddrFrom4([4]byte{1, 2, 3, 4}), Port: 443, UDP: false}
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
