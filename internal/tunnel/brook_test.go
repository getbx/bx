package tunnel

import (
	"io"
	"net"
	"testing"
)

// startFakeSocks5 起一个最小 socks5 服务器,仅用于验证 socks5Health 能握手成功。
func startFakeSocks5(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go handleSocks5(c)
		}
	}()
	return ln.Addr().String()
}

func handleSocks5(c net.Conn) {
	defer c.Close()
	buf := make([]byte, 262)
	// 1) 握手: VER NMETHODS METHODS
	if _, err := io.ReadFull(c, buf[:2]); err != nil {
		return
	}
	n := int(buf[1])
	io.ReadFull(c, buf[:n])
	c.Write([]byte{0x05, 0x00}) // 无需认证
	// 2) 请求: VER CMD RSV ATYP ...
	if _, err := io.ReadFull(c, buf[:4]); err != nil {
		return
	}
	atyp := buf[3]
	switch atyp {
	case 0x01:
		io.ReadFull(c, buf[:4+2]) // IPv4 + port
	case 0x03:
		io.ReadFull(c, buf[:1])
		l := int(buf[0])
		io.ReadFull(c, buf[:l+2])
	case 0x04:
		io.ReadFull(c, buf[:16+2])
	}
	c.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0}) // 回成功
	c.Read(buf[:1])                                            // 读一点点就关
}

func TestSocks5HealthAgainstFakeServer(t *testing.T) {
	socks := startFakeSocks5(t)
	h := socks5Health("example.com:443")
	lat, err := h(socks)
	if err != nil {
		t.Fatalf("health 失败: %v", err)
	}
	if lat < 0 {
		t.Fatalf("延迟异常: %d", lat)
	}
}
