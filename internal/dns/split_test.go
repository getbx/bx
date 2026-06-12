package dns

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestUDPForwarderRoundTrip(t *testing.T) {
	// 起一个本地 UDP 服务器,把收到的查询字节原样加个标记回送。
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	go func() {
		buf := make([]byte, 512)
		n, addr, err := pc.ReadFrom(buf)
		if err != nil {
			return
		}
		resp := append([]byte{0xAB}, buf[:n]...) // 标记 + 回显
		_, _ = pc.WriteTo(resp, addr)
	}()

	fwd := NewUDPForwarder(&net.Dialer{Timeout: 2 * time.Second})
	resp, err := fwd.Forward(context.Background(), pc.LocalAddr().String(), []byte("query!"))
	if err != nil {
		t.Fatalf("forward: %v", err)
	}
	if len(resp) != len("query!")+1 || resp[0] != 0xAB {
		t.Fatalf("bad resp: %v", resp)
	}
}
