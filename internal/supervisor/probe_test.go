package supervisor

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

type fakeProbeDialer struct {
	addr  string
	delay time.Duration
	err   error
}

func (f *fakeProbeDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	f.addr = address
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if f.err != nil {
		return nil, f.err
	}
	client, server := net.Pipe()
	go func() { _ = server.Close() }()
	return client, nil
}

func TestProbeMeasuresTheHandshake(t *testing.T) {
	dialer := &fakeProbeDialer{delay: 12 * time.Millisecond}
	got := probeServer(context.Background(), dialer, ProbeRequest{Host: "203.0.113.10", Port: 443})

	if !got.Reachable {
		t.Fatalf("判成不可达:%+v", got)
	}
	if got.RTTMS < 10 {
		t.Errorf("RTT = %dms,没量到真实耗时", got.RTTMS)
	}
	if dialer.addr != "203.0.113.10:443" {
		t.Errorf("拨到了 %q", dialer.addr)
	}
}

// **「没通」绝不能表达成 RTT=0。** 界面会显示「0 毫秒」,而那是这个仓库反复
// 禁止的那种谎:零值读起来像一切正常。
func TestUnreachableNeverLooksLikeZeroLatency(t *testing.T) {
	got := probeServer(context.Background(),
		&fakeProbeDialer{err: &net.OpError{Op: "dial", Err: errors.New("connection refused")}},
		ProbeRequest{Host: "203.0.113.10", Port: 443})

	if got.Reachable {
		t.Fatal("拨号失败却判成可达")
	}
	if got.RTTMS != 0 {
		t.Errorf("失败却带了 RTT %dms", got.RTTMS)
	}
	if got.Error == "" {
		t.Error("失败却没说原因")
	}
	if !strings.Contains(got.Error, "拒") {
		t.Errorf("原因没说清楚:%q", got.Error)
	}
}

// 握手快到量不出来时报 1ms,不报 0 —— 同上,0 读起来像「没量」。
func TestFastHandshakeNeverReportsZero(t *testing.T) {
	got := probeServer(context.Background(), &fakeProbeDialer{}, ProbeRequest{Host: "127.0.0.1", Port: 443})
	if !got.Reachable {
		t.Fatal("判成不可达")
	}
	if got.RTTMS < 1 {
		t.Errorf("RTT = %d,快到量不出来时也必须报 1ms", got.RTTMS)
	}
}

// 超时是**最常见**的失败(服务器关着就是不应答),必须说人话而不是把
// context 的错误原样吐出来。
func TestTimeoutSaysNoAnswer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got := probeServer(ctx, &fakeProbeDialer{delay: time.Second},
		ProbeRequest{Host: "203.0.113.10", Port: 443})
	if got.Reachable {
		t.Fatal("超时却判成可达")
	}
	if got.Error == "" || strings.Contains(got.Error, "context") {
		t.Errorf("原因不是人话:%q", got.Error)
	}
}

// 原始错误不外传:里面有本机接口名与路由细节,而用户需要的只是那三类之一。
func TestProbeErrorsDoNotLeakInterfaceDetails(t *testing.T) {
	got := probeServer(context.Background(), &fakeProbeDialer{
		err: &net.OpError{
			Op: "dial", Net: "tcp",
			Source: &net.TCPAddr{IP: net.ParseIP("192.168.1.42")},
			Err:    errors.New("bind: can't assign requested address on en0"),
		},
	}, ProbeRequest{Host: "203.0.113.10", Port: 443})

	for _, leak := range []string{"192.168.1.42", "en0", "bind"} {
		if strings.Contains(got.Error, leak) {
			t.Errorf("失败原因里泄漏了 %q:%q", leak, got.Error)
		}
	}
}

// 坏输入当场拒绝,**而且一个包都不发**。
func TestProbeRejectsBadInputWithoutDialing(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  ProbeRequest
	}{
		{"没有主机", ProbeRequest{Port: 443}},
		{"端口越界", ProbeRequest{Host: "203.0.113.10", Port: 70000}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dialer := &fakeProbeDialer{}
			got := probeServer(context.Background(), dialer, tc.req)
			if got.Reachable {
				t.Fatal("坏输入判成了可达")
			}
			if dialer.addr != "" {
				t.Fatalf("坏输入仍然发了一次拨号:%q", dialer.addr)
			}
		})
	}
}

// 端口留空时按 443 算(bx 的服务器绝大多数在 443),但**必须把用到的端口报回去**,
// 否则用户读到一个「通」而不知道通的是哪个口。
func TestMissingPortDefaultsAndIsReported(t *testing.T) {
	dialer := &fakeProbeDialer{}
	got := probeServer(context.Background(), dialer, ProbeRequest{Host: "203.0.113.10"})
	if got.Port != 443 {
		t.Fatalf("端口 = %d, want 443", got.Port)
	}
	if !strings.HasSuffix(dialer.addr, ":443") {
		t.Fatalf("拨到了 %q", dialer.addr)
	}
}

// **探测必须拿到 DirectDialer,不是随便一个 net.Dialer。**
//
// 这是整条路上唯一一处会**静默**出错的地方:传一个普通拨号器照样编译、照样
// 返回一个漂亮的毫秒数 —— 只不过那个数是「你 → 当前服务器 → 目标」,对
// 「该换哪一台」毫无意义。失败的信号是没有信号,正是这个仓库反复记录的形状。
//
// run.go 是组装根(单测进不去),所以这条守卫读源码。读不懂就响亮失败。
func TestProbeIsWiredToTheDirectDialer(t *testing.T) {
	source, err := os.ReadFile("run.go")
	if err != nil {
		t.Fatalf("读不到 run.go:%v", err)
	}
	text := string(source)
	// `direct` 就是 plat.DirectDialer() —— 先证明这一点,否则下面那句断言
	// 可能钉在一个同名但无关的变量上。
	if !strings.Contains(text, "direct := plat.DirectDialer()") {
		t.Fatal("run.go 里 `direct` 不再是 plat.DirectDialer() —— 守卫已经失效,先修守卫")
	}
	call := strings.Index(text, "serveControlWithPathRecovery(")
	if call < 0 {
		t.Fatal("读不出控制面的接线 —— 守卫已经失效,先修守卫")
	}
	end := strings.Index(text[call:], "\n")
	if end < 0 {
		t.Fatal("读不出那一行 —— 守卫已经失效,先修守卫")
	}
	line := text[call : call+end]
	// 最后一个实参就是 probeDial。
	if !strings.Contains(line, "recoverer, direct)") {
		t.Fatalf("探测拨号器不是 direct —— 量到的会是「你 → 当前服务器 → 目标」:\n  %s", line)
	}
}
