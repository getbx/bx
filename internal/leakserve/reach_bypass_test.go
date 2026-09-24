package leakserve

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"syscall"
	"testing"

	"github.com/getbx/bx/internal/leakcheck"
)

// —— 绕过隧道那条路(known-gaps B3,2026-09-23)——

// **名字解析必须也走物理网卡。** bx 开着时系统 DNS 是 bx 自己(fake-IP):用系统
// 解析器会拿到 198.18/15 的假地址,从物理网卡发出去就石沉大海,于是直连被判成
// 「不通」而真正的原因是我们自己问错了人。判据打在「拨号器拿到的是解析器给的 IP」
// 上,而且解析器是注入的那一个。
func TestBypassDialResolvesThroughItsOwnResolverAndDialsTheAnswer(t *testing.T) {
	var looked []string
	var dialed []string
	dial := bypassDial(
		func(_ context.Context, network, addr string) (net.Conn, error) {
			dialed = append(dialed, network+" "+addr)
			return nil, errors.New("stop here")
		},
		func(_ context.Context, host string) ([]net.IP, error) {
			looked = append(looked, host)
			return []net.IP{net.ParseIP("203.0.113.7")}, nil
		},
	)
	_, _ = dial(context.Background(), "tcp", "api.example.com:443")
	if len(looked) != 1 || looked[0] != "api.example.com" {
		t.Fatalf("没有经注入的解析器解析名字:%v", looked)
	}
	if len(dialed) != 1 || !strings.HasSuffix(dialed[0], "203.0.113.7:443") {
		t.Fatalf("拨的不是解析器给的地址:%v", dialed)
	}
}

func TestBypassDialDoesNotResolveAnIPLiteral(t *testing.T) {
	dial := bypassDial(
		func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("stop") },
		func(context.Context, string) ([]net.IP, error) {
			t.Error("IP 字面量也去解析了")
			return nil, nil
		},
	)
	_, _ = dial(context.Background(), "tcp", "203.0.113.7:443")
}

// 直连那条路上「我们自己没问出来」与「那台主机不理我们」必须分开:
// 前者判 Undetermined(没测成),后者才是 Unreachable(测了、不通)。另一个 VPN
// 在跑时 scoped 表里常常没有默认路由,绑网卡的 socket 在本机就 ENETUNREACH ——
// 把它报成「直连连不上」是一句凭空造出来的对照。
func TestBypassPathLocalFailuresAreUndeterminedNotUnreachable(t *testing.T) {
	tgt := leakcheck.ReachTarget{ID: "x", URL: "https://example.invalid/"}
	local := func(context.Context, string, string) (net.Conn, error) {
		return nil, &net.OpError{Op: "dial", Err: fmt.Errorf("connect: %w", syscall.ENETUNREACH)}
	}
	unresolved := bypassDial(
		func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("unused") },
		func(context.Context, string) ([]net.IP, error) { return nil, errors.New("i/o timeout") },
	)
	for name, dial := range map[string]DialFunc{"SYN 没离开本机": local, "物理网卡上解析不出来": unresolved} {
		got := probeOne(context.Background(), dial, tgt, leakcheck.ReachPathBypass)
		if got.State != leakcheck.ReachUndetermined {
			t.Errorf("%s:State = %v, want undetermined —— 没测成不是「不通」", name, got.State)
		}
		if got.Detail == "" || strings.Contains(got.Detail, "i/o timeout") {
			t.Errorf("%s:Detail 空着或带了原始错误:%q", name, got.Detail)
		}
	}
	// 反面:对方超时/拒绝仍然是「不通」—— 少了这一条,「直连一律判没测成」也能满足上面。
	refused := func(context.Context, string, string) (net.Conn, error) {
		return nil, &net.OpError{Op: "dial", Err: fmt.Errorf("connect: %w", syscall.ECONNREFUSED)}
	}
	if got := probeOne(context.Background(), refused, tgt, leakcheck.ReachPathBypass); got.State != leakcheck.ReachUnreachable {
		t.Errorf("对方拒绝连接被判成了 %v,want unreachable", got.State)
	}
	// 当前路径的行为一个字不改(这次只动直连那条)。
	if got := probeOne(context.Background(), local, tgt, leakcheck.ReachPathCurrent); got.State != leakcheck.ReachUnreachable {
		t.Errorf("当前路径的本机失败被改判成了 %v —— 这次改动不该碰当前路径", got.State)
	}
}

// 供货了拨号器就真的会跑两条路,预算与披露跟着一起长(同一份 deps)。
func TestWithBypassAddsTheSecondPath(t *testing.T) {
	base := LiveReachDeps()
	deps := withBypassDial(base, func(context.Context, string, string) (net.Conn, error) { return nil, nil })
	if deps.BypassDial == nil || deps.CurrentDial == nil {
		t.Fatal("加了直连之后少了一条路")
	}
	if ReachBudgetFor(deps) <= ReachBudgetFor(base) {
		t.Fatal("两条路的预算没有比一条路长 —— 后面的探测会被掐断,长得和「不通」一样")
	}
	if got := withBypassDial(ReachDeps{}, func(context.Context, string, string) (net.Conn, error) { return nil, nil }); got.BypassDial != nil {
		t.Fatal("整轮探测被关掉(--no-reach)时仍然加上了直连那条")
	}
}
