package supervisor

import (
	"context"
	"errors"
	"go/ast"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
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
