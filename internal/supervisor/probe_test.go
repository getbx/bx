package supervisor

import (
	"context"
	"errors"
	"net"
	"os"
	"regexp"
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
//
// **注意:`serveControlWithPathRecovery` 从位置参数改成了 `controlServeOptions`
// 具名字段之后(组装根瘦身,修复轮 1),原来「按位置数第几个实参」的判据机制
// 本身就不再成立——具名字段没有固定位置。这里改成按字段名 + 字面量匹配,
// 证明的事实一个字没变(ProbeDial 确实接的是 direct、ConfigWarnings 确实接的是
// riskyRuleWarnings(cfg)),变的只是「怎么在源码文本里认出这个事实」这一层
// 不可避免要跟着调用语法一起变。**
func TestProbeIsWiredToTheDirectDialer(t *testing.T) {
	block := readServeControlCallBlock(t)
	// `direct` 就是 plat.DirectDialer() —— 先证明这一点,否则下面那句断言
	// 可能钉在一个同名但无关的变量上。
	if !strings.Contains(block.fullSource, "direct := plat.DirectDialer()") {
		t.Fatal("run.go 里 `direct` 不再是 plat.DirectDialer() —— 守卫已经失效,先修守卫")
	}
	if !fieldWiredTo(block.text, "ProbeDial", "direct") {
		t.Fatalf("探测拨号器不是 direct —— 量到的会是「你 → 当前服务器 → 目标」:\n  %s", block.text)
	}
}

// **configWarnings 必须真的到达控制面,否则「危险直连规则」这条常驻告警
// 只在 Run 里算过、从没发布出去。** 同一个理由要求这条独立成守卫,而不是
// 顺带塞进上面那条——两件事各自都可能被漏接,合并断言会让其中一个的
// 失败被另一个的通过掩盖。
func TestProbeConfigWarningsAreWiredToTheControlPlane(t *testing.T) {
	block := readServeControlCallBlock(t)
	if !fieldWiredTo(block.text, "ConfigWarnings", "riskyRuleWarnings(cfg)") {
		t.Fatalf("configWarnings 没有接到 riskyRuleWarnings(cfg):\n  %s", block.text)
	}
}

// **应用流量归因必须接进控制面,否则 GET /v0/apps 恒 501。**
// 这是修复轮 1 的要害缺口本身——Task 8 只加了端点,没有从 Run 把真实的
// *AppTraffic 传进去,结果菜单→Guardian→Core 整条链通到一个恒 501 的端点,
// 而每一层各自的测试都是绿的。run.go 是组装根、单测进不去,只能读源码钉住。
func TestProbeAppTrafficIsWiredToTheControlPlane(t *testing.T) {
	block := readServeControlCallBlock(t)
	if !strings.Contains(block.fullSource, "appTraffic := NewAppTraffic(") {
		t.Fatal("run.go 里再找不到 appTraffic := NewAppTraffic(...) —— 守卫已经失效,先修守卫")
	}
	if !fieldWiredTo(block.text, "AppTraffic", "appTraffic") {
		t.Fatalf("controlServeOptions.AppTraffic 没有接到 run.go 构造的 appTraffic —— GET /v0/apps 会恒 501:\n  %s", block.text)
	}
}

type serveControlCallBlock struct {
	fullSource string // run.go 全文,供「先证明某变量确实是什么」的前置断言使用
	text       string // 从 serveControlWithPathRecovery( 到其收尾 "})" 的那一段
}

// readServeControlCallBlock 读 run.go、定位 serveControlWithPathRecovery 那次调用,
// 摘出 controlServeOptions{...} 结构体字面量的完整文本(从调用起点到第一个
// 独占一行、trim 后等于 "})" 的收尾行)。按行扫描而不是按固定 tab 数的字符串
// 匹配,是为了不被缩进层级的细节绊倒——这段调用套在
// requireControlSocket(func(){...}) 的匿名函数里,外层还有一个形状相同的
// "})" 收尾,必须停在第一个遇到的那个(结构体+调用的收尾),而不是外层闭包的。
func readServeControlCallBlock(t *testing.T) serveControlCallBlock {
	t.Helper()
	source, err := os.ReadFile("run.go")
	if err != nil {
		t.Fatalf("读不到 run.go:%v", err)
	}
	text := string(source)
	call := strings.Index(text, "serveControlWithPathRecovery(")
	if call < 0 {
		t.Fatal("读不出控制面的接线 —— 守卫已经失效,先修守卫")
	}
	lines := strings.Split(text[call:], "\n")
	var b strings.Builder
	found := false
	for i, line := range lines {
		b.WriteString(line)
		b.WriteString("\n")
		if i > 0 && strings.TrimSpace(line) == "})" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("读不出那次调用的收尾 —— 守卫已经失效,先修守卫")
	}
	return serveControlCallBlock{fullSource: text, text: b.String()}
}

// fieldWiredTo 判断 controlServeOptions{...} 字面量里,某个具名字段是否被
// 赋值成给定的字面量表达式(如 "direct"、"riskyRuleWarnings(cfg)")。
// 字段名与值之间允许任意空白(gofmt 会按最长字段名对齐冒号后的空格),
// 值后面必须紧跟一个逗号或换行,防止 "ProbeDial: directX" 这种前缀误配。
func fieldWiredTo(block, field, value string) bool {
	re := regexp.MustCompile(regexp.QuoteMeta(field) + `:\s*` + regexp.QuoteMeta(value) + `\s*[,\n]`)
	return re.MatchString(block)
}
