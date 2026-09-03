package tunnel

import (
	"io"
	"net"
	"testing"
	"time"
)

// startFakeSocks5 起一个最小 socks5 服务器,仅用于验证 socks5Health 能握手成功。
func startFakeSocks5WithDelay(t *testing.T, delay time.Duration) string {
	return startFakeSocks5Full(t, true, delay)
}

func startFakeSocks5(t *testing.T, echo bool) string {
	return startFakeSocks5Full(t, echo, 0)
}

func startFakeSocks5Full(t *testing.T, echo bool, delay time.Duration) string {
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
			go handleSocks5(c, echo, delay)
		}
	}()
	return ln.Addr().String()
}

func handleSocks5(c net.Conn, echo bool, delay time.Duration) {
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
	if !echo {
		// **应答成功但一个字节都不回** —— 这正是 2026-09-03 真机上那个失效:
		// QUIC 开流是纯本地动作,SOCKS 立刻说 ok 而对面还没收到任何东西。
		// 隧道在这种服务器面前**必须判不健康**。
		c.Read(buf[:1])
		return
	}
	// 有个真的对面:把客户端发来的第一段原样回一点,证明一个来回穿过去了。
	if n, err := c.Read(buf); err == nil && n > 0 {
		time.Sleep(delay)
		c.Write(buf[:1])
	}
	c.Read(buf[:1])
}

// 对面真的回了字节 ⇒ 健康。
func TestSocks5HealthAgainstFakeServer(t *testing.T) {
	socks := startFakeSocks5(t, true)
	h := socks5Health("example.com:443")
	lat, err := h(socks)
	if err != nil {
		t.Fatalf("health 失败: %v", err)
	}
	if lat < 0 {
		t.Fatalf("延迟异常: %d", lat)
	}
}

// **应答了 CONNECT 但一个字节都不回 ⇒ 不健康。**
//
// 这是整条修复的全部内容。真机 2026-09-03 同一台机器、同一时刻、同一台服务器:
// reality(纯 TCP)量出 4091ms 真 RTT,而 hysteria2(QUIC)5/5 次都是 **0ms**
// —— QUIC 开一条流是纯本地动作,sing-box 流一建好就回 SOCKS 成功,服务器一个
// 字节都还没回。于是那台机器同时显示「隧道健康 0ms」与「UDP 加速档 152/152
// 回落(那台 UDP 服务器不健康)」:同一个端点,两条腿判出相反的结论。
//
// **「能建立一条连接」严格弱于「数据流得动」**,而这个仓库为此栽过三次
// (2026-06-30 health 绿 curl exit 28 / 2026-08-14 必定关闭的端口经 SOCKS5
// 一样通 / 这一次),前两次都只当排查技巧记下了。
func TestSocks5HealthRefusesAConnectionThatCarriesNothing(t *testing.T) {
	socks := startFakeSocks5(t, false)
	h := socks5Health("example.com:443")
	if _, err := h(socks); err == nil {
		t.Fatal("SOCKS 说 ok 而对面一个字节都没回,却判成了健康 —— 这正是那个假绿灯")
	}
}

// **延迟量的是「第一个字节回来」,不是「CONNECT 返回」。**
// 后者在 QUIC 上量的是本地开流 —— 那正是 0ms 的来源,而 0ms 在跨太平洋链路上
// 是不可能的读数。这条钉住那个语义:假服务器在回字节之前先睡一下,
// 读数必须包含那段时间。
func TestSocks5HealthMeasuresTimeToFirstByteNotConnect(t *testing.T) {
	socks := startFakeSocks5WithDelay(t, 120*time.Millisecond)
	lat, err := socks5Health("example.com:443")(socks)
	if err != nil {
		t.Fatal(err)
	}
	if lat < 100 {
		t.Errorf("延迟 %dms —— 没有把「等第一个字节」那段算进去(量成了本地 CONNECT)", lat)
	}
}

// **判据是「有没有字节回来」,不是「TLS 握手成不成功」。**
// 后者会把一个不说 TLS 的探测目标判成隧道故障,而我们要证明的只有一件事:
// 一个来回真的穿过了隧道。上面那条 echo=true 的假服务器回的是一个**不是
// 合法 TLS 记录**的字节,握手必然失败 —— 它照样必须判健康。
func TestSocks5HealthAcceptsAnyBytesNotJustValidTLS(t *testing.T) {
	socks := startFakeSocks5(t, true)
	if _, err := socks5Health("example.com:443")(socks); err != nil {
		t.Fatalf("对面回了字节(只是不是合法 TLS)却判成不健康:%v", err)
	}
}
