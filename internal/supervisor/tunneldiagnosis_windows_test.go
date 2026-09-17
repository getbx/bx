//go:build windows

package supervisor

import (
	"context"
	"errors"
	"net"
	"os"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"
)

// Windows 那半的行为断言。它只在 CI 的 windows runner 上跑(verify.sh 对 Windows
// 只交叉编译),配套的可移植守卫是 TestEveryLocalDialFailureHasAWinsockTwin。
//
// 前半段先把**陷阱本身**钉住:少了它,哪天 Go 真的让 syscall.Errno.Is 桥接
// 了 WSAE 一族,这条测试会一直绿着,而绿的理由已经换成了另一件事。
func TestWinsockLocalDialFailuresAreNotReportedAsTheServerNotAnswering(t *testing.T) {
	if syscall.ENETUNREACH == windows.WSAENETUNREACH {
		t.Fatal("syscall.ENETUNREACH 与 WSAENETUNREACH 相等了 —— 这条测试的前提没了,回去重读判据")
	}
	if errors.Is(windows.WSAENETUNREACH, syscall.ENETUNREACH) {
		t.Fatal("syscall.Errno.Is 现在跨映射了 —— 那张孪生表可以退场,但要有人先确认所有四个都被映射")
	}

	cause := tagStartFailure(ErrTunnelUnhealthy, errors.New("健康检查超时"))
	addr := closedLocalAddress(t)
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"WSAENETUNREACH", windows.WSAENETUNREACH},
		{"WSAEHOSTUNREACH", windows.WSAEHOSTUNREACH},
		{"WSAEACCES", windows.WSAEACCES},
		{"WSAEADDRNOTAVAIL", windows.WSAEADDRNOTAVAIL},
	} {
		dialer := func() tunnelDialFunc {
			return func(_ context.Context, network, address string) (net.Conn, error) {
				return nil, &net.OpError{Op: "dial", Net: network, Err: os.NewSyscallError("connect", tc.err)}
			}
		}
		err := diagnoseUnhealthyTunnel(context.Background(), addr, dialer, cause)
		// **期望的是 …LocalDial 那个码,与 darwin/linux 那条兄弟测试一字同源。**
		// 它此前停在更笼统的 StartFailureTunnelUndetermined 上 —— 「五种结局」
		// 那一轮给「SYN 没离开本机」单独立了码,而这条测试没跟上。
		// **没跟上的原因是它从来没被执行过**:verify.sh 对 Windows 只 typecheck
		// (编得过就绿),而 CI 的 windows 腿在别的事情上红着,于是没人看见。
		// 一条编得过、看起来对、却一次都没跑过的守卫,与没有这条守卫一样。
		if got := StartFailureCode(err); got != StartFailureTunnelUndeterminedLocalDial {
			t.Fatalf("%s 分类成 %q,want %q —— 这次失败发生在本机,SYN 一个都没出去,\n"+
				"说「那台服务器没有应答」是替一个我们根本没做过的观测下结论",
				tc.name, got, StartFailureTunnelUndeterminedLocalDial)
		}
	}

	// 反向(与 darwin/linux 那条一字同源):被拒绝仍然是 tunnel_unreachable ——
	// 少了这一半,「凡是拨不通一律判不出来」也能满足上面那几条。
	refused := func() tunnelDialFunc {
		return func(_ context.Context, network, address string) (net.Conn, error) {
			return nil, &net.OpError{Op: "dial", Net: network, Err: os.NewSyscallError("connect", windows.WSAECONNREFUSED)}
		}
	}
	if got := StartFailureCode(diagnoseUnhealthyTunnel(context.Background(), addr, refused, cause)); got != StartFailureTunnelUnreachable {
		t.Fatalf("connection refused 分类成 %q,want %q —— 那是那台服务器给的答复",
			got, StartFailureTunnelUnreachable)
	}
}
