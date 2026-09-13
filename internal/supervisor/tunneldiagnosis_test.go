package supervisor

import (
	"context"
	"errors"
	"go/ast"
	"net"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/getbx/bx/internal/tunnel"
)

// 三种结局各一条,判据是**渲染出来的那句话**与分类码两样都要对得上。
//
// 只比枚举值挡不住这次要消灭的东西:把三个分支映射到同一句「隧道没起来」
// 照样全绿,而所有者的原话正是「bx 不会告诉我是 vps 不通」。
func TestUnhealthyTunnelIsSplitIntoThreeOutcomesThatReadDifferently(t *testing.T) {
	cause := tagStartFailure(ErrTunnelUnhealthy, errors.New("bx 隧道健康检查超时(20s): restarts=0"))

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	answering := listener.Addr().String()
	closedPort := closedLocalAddress(t)

	// 生产形状的拨号器:真的去连一个真的 TCP 端口。
	realDialer := func() tunnelDialFunc { return (&net.Dialer{}).DialContext }

	unreachable := diagnoseUnhealthyTunnel(context.Background(), closedPort, realDialer, cause)
	handshake := diagnoseUnhealthyTunnel(context.Background(), answering, realDialer, cause)
	// 判别本身没答出来:拨号一直挂着,直到我们自己的预算耗尽。
	blocked := func() tunnelDialFunc {
		return func(ctx context.Context, _, _ string) (net.Conn, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}
	}
	tightCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	undetermined := diagnoseUnhealthyTunnel(tightCtx, closedPort, blocked, cause)

	cases := []struct {
		name string
		err  error
		code string
	}{
		{"连不上", unreachable, StartFailureTunnelUnreachable},
		{"连得上但没握上手", handshake, StartFailureTunnelHandshakeFailed},
		{"没判出来", undetermined, StartFailureTunnelUndetermined},
	}
	seen := map[string]string{}
	for _, tc := range cases {
		if tc.err == nil {
			t.Fatalf("%s:判别把一次失败的启动说成了成功", tc.name)
		}
		if got := StartFailureCode(tc.err); got != tc.code {
			t.Fatalf("%s 分类成 %q,want %q(错误:%v)", tc.name, got, tc.code, tc.err)
		}
		// 三档都还在「隧道没起来」这一族里 —— 具体那两档必须排在家长前面,
		// 否则三档塌成一档而这条断言恰恰会先红。
		if !errors.Is(tc.err, ErrTunnelUnhealthy) {
			t.Fatalf("%s 掉出了 ErrTunnelUnhealthy 这一族", tc.name)
		}
		if prev, dup := seen[tc.err.Error()]; dup {
			t.Fatalf("%s 与 %s 说的是同一句话:\n  %s\n"+
				"两者的处置完全相反,压成一句等于把用户派去修一台好机器", tc.name, prev, tc.err.Error())
		}
		seen[tc.err.Error()] = tc.name
	}
	// 说得出是哪台服务器 —— 事故里缺的就是这个。
	if !strings.Contains(unreachable.Error(), closedPort) {
		t.Fatalf("「连不上」没点名是哪台服务器:%v", unreachable)
	}
	if !strings.Contains(handshake.Error(), answering) {
		t.Fatalf("「连得上」没点名是哪台服务器:%v", handshake)
	}
	// 「没判出来」绝不许自称是两个具体答案里的任何一个。
	if errors.Is(undetermined, ErrTunnelUnreachable) || errors.Is(undetermined, ErrTunnelHandshakeFailed) {
		t.Fatalf("判别失败时挑了一个答案:%v", undetermined)
	}
}

// 判别不出来的另外两种形状,一样落「没判出来」:链接里解不出 host:port、
// 以及父 ctx 在拨号途中被取消(**是我们自己没问完,不是那台服务器没答**)。
func TestDiagnosisThatCannotAskFallsBackToUndetermined(t *testing.T) {
	cause := tagStartFailure(ErrTunnelUnhealthy, errors.New("健康检查超时"))

	unparsable := diagnoseUnhealthyTunnel(context.Background(), "vless://", func() tunnelDialFunc {
		return func(context.Context, string, string) (net.Conn, error) {
			t.Error("链接都解不出 host:port,不该拨号")
			return nil, errors.New("不该到这里")
		}
	}, cause)
	if got := StartFailureCode(unparsable); got != StartFailureTunnelUndetermined {
		t.Fatalf("解不出地址时分类成 %q,want %q", got, StartFailureTunnelUndetermined)
	}

	canceled, cancel := context.WithCancel(context.Background())
	interrupted := diagnoseUnhealthyTunnel(canceled, closedLocalAddress(t), func() tunnelDialFunc {
		return func(ctx context.Context, _, _ string) (net.Conn, error) {
			cancel()
			return nil, errors.New("dial tcp: operation was canceled")
		}
	}, cause)
	if got := StartFailureCode(interrupted); got != StartFailureTunnelUndetermined {
		t.Fatalf("判别被打断时分类成 %q,want %q —— 父 ctx 挂了是我们自己没问完,\n"+
			"报「连不上服务器」等于替一次没做完的观测下结论", got, StartFailureTunnelUndetermined)
	}
}

// **判别只在失败路径上发生。**
//
// 守的是所有者定死的那条边界(不后台定时探测):这次拨号走在隧道外面、目的地
// 是用户的服务器,它只许由一次失败触发。判据是注入的拨号器(与它的 thunk)
// 在成功那条路上**一次都没有被碰过** —— 用记录型桩而不是 t.Fatal 的桩:
// 后者一响就先失败在别处,真正想验的那条断言反而够不着。
func TestHealthyStartupNeverDials(t *testing.T) {
	var mu sync.Mutex
	dialerHandouts, dials := 0, 0
	dialer := func() tunnelDialFunc {
		mu.Lock()
		dialerHandouts++
		mu.Unlock()
		return func(context.Context, string, string) (net.Conn, error) {
			mu.Lock()
			dials++
			mu.Unlock()
			return nil, errors.New("不该拨号")
		}
	}

	healthy, runner := newHealthySwapTunnel("127.0.0.1:10099")
	healthy.Start()
	defer func() { _ = runner.Kill() }()
	defer healthy.Stop()

	if err := awaitTunnelHealthOrDiagnose(context.Background(), healthy, 2*time.Second,
		"198.51.100.7:443", dialer); err != nil {
		t.Fatalf("健康的隧道被判成失败:%v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if dials != 0 || dialerHandouts != 0 {
		t.Fatalf("成功启动的路径上拨了 %d 次号、造了 %d 次拨号器 —— 判别只许由失败触发\n"+
			"(darwin 上造一次拨号器还要 exec 一个 route 进程)", dials, dialerHandouts)
	}
}

// ctx 被取消不是「隧道没起来」,不许为它拨号,也不许被分类成隧道的任何一档。
func TestCanceledStartupIsNotDiagnosedAsATunnelOutcome(t *testing.T) {
	dials := 0
	dialer := func() tunnelDialFunc {
		return func(context.Context, string, string) (net.Conn, error) {
			dials++
			return nil, errors.New("不该拨号")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := awaitTunnelHealthOrDiagnose(ctx, newNeverHealthyTunnel(), time.Second, "198.51.100.7:443", dialer)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if dials != 0 {
		t.Fatalf("关机途中拨了 %d 次号", dials)
	}
	if code := StartFailureCode(err); code == StartFailureTunnelUnreachable || code == StartFailureTunnelHandshakeFailed {
		t.Fatalf("关机被分类成 %q", code)
	}
}

// 接线:Run() 必须走带判别那条路。
//
// 判据是**语义位置**而不是「文件里出现过这个名字」——
//   - Run() 里不许再直接 waitTunnelHealthy(那条路没有判别,而它就在隔壁,
//     是最自然的一次「顺手改回去」);
//   - 那次调用要拿着 cfg.Server(判别要知道拨哪台)与一个真从
//     plat.DirectDialer() 取拨号器的 thunk(拿别的拨号器 = 可能绕回隧道成环)。
func TestRunAwaitsTunnelHealthThroughTheDiagnosingPath(t *testing.T) {
	fn := runFuncDecl(t)
	if callsFunction(fn, "waitTunnelHealthy") {
		t.Fatal("Run() 又直接调 waitTunnelHealthy 了 —— 那条路不判别,\n" +
			"「VPS 不通」与「链接坏了」会重新塌成同一句话")
	}
	call := findCall(t, fn, "awaitTunnelHealthOrDiagnose")
	if len(call.Args) != 5 {
		t.Fatalf("awaitTunnelHealthOrDiagnose 收到 %d 个实参(接线守卫失效,请更新本测试)", len(call.Args))
	}
	requireSelector(t, call.Args[3], "cfg", "Server", "判别要知道该拨哪台服务器")
	lit, ok := call.Args[4].(*ast.FuncLit)
	if !ok {
		t.Fatalf("拨号器实参必须是一个 thunk(闭包),实际是 %T —— 直接传拨号器会让\n"+
			"成功启动的路径也去造它(darwin 上要 exec 一个 route 进程)", call.Args[4])
	}
	if !mentionsSelector(lit, "plat", "DirectDialer") {
		t.Fatal("判别用的拨号器不是 plat.DirectDialer() —— 别的拨号器可能绕回隧道成环")
	}
}

// listeningLocalAddress 起一个真的在应答的本地端口,并在测试结束时收掉。
func listeningLocalAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	return listener.Addr().String()
}

func closedLocalAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

func callsFunction(fn ast.Node, name string) bool {
	found := false
	ast.Inspect(fn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); ok && id.Name == name {
			found = true
		}
		return true
	})
	return found
}

func findCall(t *testing.T, fn ast.Node, name string) *ast.CallExpr {
	t.Helper()
	var found *ast.CallExpr
	ast.Inspect(fn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || found != nil {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); ok && id.Name == name {
			found = call
		}
		return true
	})
	if found == nil {
		t.Fatalf("Run() 里找不到对 %s 的调用(接线守卫失效,请更新本测试)", name)
	}
	return found
}

func mentionsSelector(n ast.Node, x, sel string) bool {
	found := false
	ast.Inspect(n, func(node ast.Node) bool {
		s, ok := node.(*ast.SelectorExpr)
		if !ok || s.Sel.Name != sel {
			return true
		}
		if id, ok := s.X.(*ast.Ident); ok && id.Name == x {
			found = true
		}
		return true
	})
	return found
}

// C2:hysteria2 跑在 QUIC/UDP 上 —— 一次 TCP 拨号观测不到它。
//
// 它是**被支持的主传输**(transportKind 认它、`bx server install --protocol
// hysteria2` 装它、CLAUDE.md 记着真机 e2e 过)。一台活得好好的 hysteria2 服务器
// 不会应答 TCP SYN,而 `bx server deploy` 把 443/tcp 也放行了,所以连「拒绝」
// 都不是:那次拨号一路超时 ⇒ tunnel_unreachable ⇒ 对着一台**正常的**服务器说
// 「它可能挂了、或者换了 IP」。这是确定性的,不是概率性的。
func TestAUDPOnlyTransportIsNeverProbedWithTCPAndFallsToUndetermined(t *testing.T) {
	cause := tagStartFailure(ErrTunnelUnhealthy, errors.New("bx 隧道健康检查超时(20s): restarts=0"))
	// 端口是关着的 —— 拨过去必定超时/被拒,也就是会被判成「那台机器挂了」。
	closed := closedLocalAddress(t)
	dials := 0
	dialer := func() tunnelDialFunc {
		return func(context.Context, string, string) (net.Conn, error) {
			dials++
			return nil, errors.New("不该拨号")
		}
	}
	for _, link := range []string{
		"hysteria2://secret@" + closed + "?insecure=1",
		"hy2://secret@" + closed,
	} {
		err := diagnoseUnhealthyTunnel(context.Background(), link, dialer, cause)
		if got := StartFailureCode(err); got != StartFailureTunnelUndeterminedUDPTransport {
			t.Fatalf("%s 分类成 %q,want %q —— 一次 TCP 拨号不是对一台 QUIC 服务器的观测",
				transportKind(link), got, StartFailureTunnelUndeterminedUDPTransport)
		}
		// 它仍然是「没判出来」那一族:消费方漏掉这个具体来由时落回笼统那句话,
		// 而不会落进两个具体答案里的任何一个。
		if !IsTunnelUndeterminedCode(StartFailureCode(err)) {
			t.Fatalf("%s 的码离开了「没判出来」那一族:%q", transportKind(link), StartFailureCode(err))
		}
		if errors.Is(err, ErrTunnelUnreachable) || errors.Is(err, ErrTunnelHandshakeFailed) {
			t.Fatalf("对一台没在 TCP 上听的服务器挑了一个具体答案:%v", err)
		}
	}
	if dials != 0 {
		t.Fatalf("对 UDP 传输拨了 %d 次 TCP —— 那次拨号观测不到任何东西,只会给出一个错答案", dials)
	}
}

// 上面那条判据必须覆盖**每一种** transportKind 会返回的传输,而不是「今天记得
// 的那几种」。少一种就是一个静默的错答案。
//
// **穷举来自 tunnel.Kinds(),不是这里再抄一份。** 上一版这条测试自称穷举,而它
// 比对的是它自己手写的一张六行的表 —— 变异实测(LANDED):给 tunnel.Kind 加一种
// tuic,internal/tunnel 与 internal/supervisor **两个包全绿**,那句「每一种」当场
// 变成假话。后果被极性兜住(没登记 ⇒ 判不出来 ⇒ 丢一个答案,不是编一个),
// 所以坏的是那句陈述而不是代码 —— 而本仓库对「关于代码的假陈述」的处置是根治:
// 清单下沉 internal/tunnel(与 internal/udpsource、internal/barriercidr 同一先例),
// 于是 Kind 与这张表在构造上不可能各说各的。
func TestEveryTransportKindDeclaresWhetherATCPProbeObservesIt(t *testing.T) {
	kinds := tunnel.Kinds()
	if len(kinds) == 0 {
		t.Fatal("tunnel.Kinds() 是空的 —— 这条守卫此刻一种传输都没检查")
	}
	for _, kind := range kinds {
		if _, ok := transportsAnsweringTCP[kind]; !ok {
			t.Fatalf("传输 %q 没有登记「一次 TCP 拨号观测不观测得到它」——\n"+
				"没登记就落 false(判不出来),那是对的极性,但沉默地落进去说明没有人想过这个问题", kind)
		}
	}
	// transportKind 只是 tunnel.Kind 的薄壳 —— 少了这一条,上面那圈穷举可能问的
	// 是一个跟生产判据没关系的清单。
	if got := transportKind("hysteria2://p@example.com:443"); got != tunnel.KindHysteria2 {
		t.Fatalf("transportKind 与 tunnel.Kind 对不上:得到 %q,want %q", got, tunnel.KindHysteria2)
	}
	if transportsAnsweringTCP[tunnel.KindHysteria2] {
		t.Fatal("hysteria2 被登记成「TCP 上有东西在听」—— 它是 QUIC/UDP")
	}
	// 认不出的种类必须落「判不出来」那一档,不许默认成「可以拨」。
	if transportsAnsweringTCP["某种将来的传输"] {
		t.Fatal("没登记的传输默认成了「TCP 上有东西在听」")
	}
}

// I3:**本机自己**没能把 SYN 发出去,不许说成「那台服务器的端口没有应答」。
//
// ENETUNREACH 有真机先例:DirectDialer 的 IP_BOUND_IF 只查 scoped 路由表,而
// 那条 scoped 默认路由是 Hijack(run.go:880)装的,判别拨号在 run.go:308,
// **早 572 行**。一台 macOS 上没有 per-interface default 的机器上,每一次判别
// 拨号都在本地就 network is unreachable —— 于是不管 VPS 在做什么,bx 都答
// 「你的 VPS 挂了」。2026-08-13 那次事故的同一签名,落在唯一一条职责就是
// 说实话的路上。
func TestALocalDialFailureIsNotReportedAsTheServerNotAnswering(t *testing.T) {
	cause := tagStartFailure(ErrTunnelUnhealthy, errors.New("健康检查超时"))
	addr := closedLocalAddress(t)
	local := []struct {
		name string
		err  error
	}{
		{"network is unreachable", syscall.ENETUNREACH},
		{"no route to host", syscall.EHOSTUNREACH},
		{"permission denied", syscall.EACCES},
		{"cannot assign requested address", syscall.EADDRNOTAVAIL},
	}
	for _, tc := range local {
		dialer := func() tunnelDialFunc {
			return func(_ context.Context, network, address string) (net.Conn, error) {
				return nil, &net.OpError{Op: "dial", Net: network, Err: os.NewSyscallError("connect", tc.err)}
			}
		}
		err := diagnoseUnhealthyTunnel(context.Background(), addr, dialer, cause)
		if got := StartFailureCode(err); got != StartFailureTunnelUndeterminedLocalDial {
			t.Fatalf("%s 分类成 %q,want %q —— 这次失败发生在本机,SYN 一个都没出去,\n"+
				"说「那台服务器没有应答」是替一个我们根本没做过的观测下结论", tc.name, got, StartFailureTunnelUndeterminedLocalDial)
		}
		// 它有自己的码是因为**处置不同**(要查 bx 自己的直连器,不是 VPS),
		// 但它仍属「没判出来」那一族 —— 谁都不许把它读成「服务器没事」。
		if !IsTunnelUndeterminedCode(StartFailureCode(err)) {
			t.Fatalf("%s 的码离开了「没判出来」那一族:%q", tc.name, StartFailureCode(err))
		}
	}
	// 反向:被拒绝与超时仍然是 tunnel_unreachable(台账 ruling ②,一个字没动)——
	// 少了这一半,「凡是拨不通一律判不出来」也能满足上面那几条,而那等于把这一支
	// 要给用户的那个答案整个扔掉。
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"connection refused", syscall.ECONNREFUSED},
		{"i/o timeout", os.ErrDeadlineExceeded},
	} {
		dialer := func() tunnelDialFunc {
			return func(_ context.Context, network, address string) (net.Conn, error) {
				return nil, &net.OpError{Op: "dial", Net: network, Addr: nil, Err: tc.err}
			}
		}
		err := diagnoseUnhealthyTunnel(context.Background(), addr, dialer, cause)
		if got := StartFailureCode(err); got != StartFailureTunnelUnreachable {
			t.Fatalf("%s 分类成 %q,want %q —— 事故原文正是 i/o timeout", tc.name, got, StartFailureTunnelUnreachable)
		}
	}
}

// M2:解不出 host:port 时,那句话里**不许**带上原始错误 —— url.Parse 的
// *url.Error 会原样打印整条链接,vless 的 UUID 就在里面。这条消息进 Core 日志,
// 而批二要把这一族往 Guardian 送:一条凭据一旦进了会被转发的字符串就收不回来。
func TestDiagnosisNeverPrintsTheServerLink(t *testing.T) {
	const uuid = "3f2b9c7e-dead-beef-cafe-000000000001"
	link := "vless://" + uuid + "@ho st:443?security=reality&sni=www.cloudflare.com"
	// 前提自检:那条链接确实解不出地址,而原始错误确实带着 UUID ——
	// 少了这一步,这条守卫可能只是因为走了别的分支而绿。
	_, parseErr := serverDialAddress(link)
	if parseErr == nil {
		t.Fatal("这条链接现在解得出地址了,守卫要换一条")
	}
	if !strings.Contains(parseErr.Error(), uuid) {
		t.Fatalf("原始错误里已经没有 UUID 了 —— 这条守卫守的东西可能已经换了地方:%v", parseErr)
	}

	err := diagnoseUnhealthyTunnel(context.Background(), link, func() tunnelDialFunc {
		return func(context.Context, string, string) (net.Conn, error) {
			t.Error("链接都解不出 host:port,不该拨号")
			return nil, errors.New("不该到这里")
		}
	}, tagStartFailure(ErrTunnelUnhealthy, errors.New("健康检查超时")))
	if strings.Contains(err.Error(), uuid) {
		t.Fatalf("判别把服务器链接里的凭据写进了错误文本:%v", err)
	}
	if strings.Contains(err.Error(), "vless://") {
		t.Fatalf("判别把整条服务器链接写进了错误文本:%v", err)
	}
	if got := StartFailureCode(err); got != StartFailureTunnelUndetermined {
		t.Fatalf("分类成 %q,want %q", got, StartFailureTunnelUndetermined)
	}
}

// 「没判出来」拆成三个码之后,**每一个都必须还认得出是「没判出来」**。
//
// 拆开的理由是处置不同(UDP 传输要换个手段确认那台机器、本机拨号失败要去查
// bx 自己的直连器),而拆开的风险恰恰相反:某个消费方漏掉一个新码,落进
// 「服务器活着」或者「服务器挂了」两个具体答案里的任何一个 —— 那正是这一支
// 存在要消灭的东西。前缀由拼接得来(不是三个字面量),这条守卫走的是**五条
// 真实的生产路径**,不是一张枚举清单。
func TestEveryUndeterminedOutcomeStillReadsAsUndetermined(t *testing.T) {
	cause := tagStartFailure(ErrTunnelUnhealthy, errors.New("bx 隧道健康检查超时(20s): restarts=0"))
	closed := closedLocalAddress(t)
	neverDial := func() tunnelDialFunc {
		return func(context.Context, string, string) (net.Conn, error) {
			return nil, errors.New("不该拨号")
		}
	}
	canceled, cancel := context.WithCancel(context.Background())

	cases := []struct {
		name string
		err  error
	}{
		{"UDP 传输", diagnoseUnhealthyTunnel(context.Background(), "hysteria2://s@"+closed, neverDial, cause)},
		{"SYN 没离开本机", diagnoseUnhealthyTunnel(context.Background(), closed, func() tunnelDialFunc {
			return func(_ context.Context, network, _ string) (net.Conn, error) {
				return nil, &net.OpError{Op: "dial", Net: network, Err: os.NewSyscallError("connect", syscall.ENETUNREACH)}
			}
		}, cause)},
		{"解不出 host:port", diagnoseUnhealthyTunnel(context.Background(), "vless://", neverDial, cause)},
		{"域名解析失败", diagnoseUnhealthyTunnel(context.Background(), closed, func() tunnelDialFunc {
			return func(context.Context, string, string) (net.Conn, error) {
				return nil, &net.DNSError{Err: "no such host", Name: "vps.example"}
			}
		}, cause)},
		{"判别本身被打断", diagnoseUnhealthyTunnel(canceled, closed, func() tunnelDialFunc {
			return func(context.Context, string, string) (net.Conn, error) {
				cancel()
				return nil, errors.New("dial tcp: operation was canceled")
			}
		}, cause)},
	}
	seen := map[string]bool{}
	for _, tc := range cases {
		code := StartFailureCode(tc.err)
		if !IsTunnelUndeterminedCode(code) {
			t.Fatalf("%s 的码 %q 不在「没判出来」那一族 —— 一个漏掉它的消费方会把它\n"+
				"读成两个具体答案里的某一个", tc.name, code)
		}
		if errors.Is(tc.err, ErrTunnelUnreachable) || errors.Is(tc.err, ErrTunnelHandshakeFailed) {
			t.Fatalf("%s 的错误链上挂着一个具体结局:%v", tc.name, tc.err)
		}
		// 家长必须还在链上:别处按 errors.Is(err, ErrTunnelUnhealthy) 做的判定
		// (awaitTunnelHealthOrDiagnose 自己就有一处)不许因为这次拆分而失效。
		if !errors.Is(tc.err, ErrTunnelUnhealthy) {
			t.Fatalf("%s 的错误链上没有家长 ErrTunnelUnhealthy:%v", tc.name, tc.err)
		}
		seen[code] = true
	}
	if len(seen) != 3 {
		t.Fatalf("五条生产路径只产出了 %d 个不同的码(%v)—— 处置不同的那两种必须\n"+
			"各有自己的码,否则用户被派去查错的东西", len(seen), seen)
	}
}
