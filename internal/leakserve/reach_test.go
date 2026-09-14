package leakserve

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/getbx/bx/internal/leakcheck"
)

// 探测的结论必须来自 leakcheck.JudgeReach,而不是在这里重写一遍判据。
func TestProbeReachUsesTheSharedJudgement(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(405)
		_, _ = w.Write([]byte(`{"type":"error","error":{"message":"Method Not Allowed"}}`))
	}))
	defer srv.Close()

	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
	}
	got := probeOne(context.Background(), dial, leakcheck.ReachTarget{
		ID: "x", URL: srv.URL,
	}, leakcheck.ReachPathCurrent)
	if got.State != leakcheck.ReachReachable {
		t.Fatalf("State = %v, want reachable —— 405 + 服务 JSON 是最强的可达证据", got.State)
	}
	if got.Path != leakcheck.ReachPathCurrent {
		t.Fatalf("Path = %q, want %q", got.Path, leakcheck.ReachPathCurrent)
	}
}

// 拨不通要判 unreachable,而且**不许把错误原文带给用户** ——
// 它可能含内网地址、接口名。
func TestProbeReachOnDialFailureSaysNothingAboutTheError(t *testing.T) {
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		return nil, &net.OpError{Op: "dial", Err: net.UnknownNetworkError("boom /private/var/secret")}
	}
	got := probeOne(context.Background(), dial, leakcheck.ReachTarget{
		ID: "x", URL: "https://example.invalid/",
	}, leakcheck.ReachPathBypass)
	if got.State != leakcheck.ReachUnreachable {
		t.Fatalf("State = %v, want unreachable", got.State)
	}
	if strings.Contains(got.Detail, "/private/var/secret") {
		t.Fatalf("Detail 带出了原始错误:%q", got.Detail)
	}
}

// 不许跟随重定向 —— 跟过去之后 status/body 是**终点**的,而这条记录仍然标着
// **起点**的 TargetID,那就是「记录 A 的观测、归因给 B」。3xx 必须落进
// JudgeReach 的「认不出的状态码 ⇒ Undetermined」那一支,不许因为跟过去拿到了
// 200 就报可达。判据打在结论上,不是打在「CheckRedirect 字段存在」上。
func TestProbeReachDoesNotFollowRedirectsToAvoidMisattribution(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	})
	mux.HandleFunc("/elsewhere", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"type":"error","error":{"message":"this is some other service"}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
	}
	got := probeOne(context.Background(), dial, leakcheck.ReachTarget{
		ID: "x", URL: srv.URL + "/start",
	}, leakcheck.ReachPathCurrent)
	if got.State != leakcheck.ReachUndetermined {
		t.Fatalf("State = %v, want undetermined —— 跟了重定向、把终点的应答"+
			"记成了起点的观测", got.State)
	}
}

// 默认值是 spec §5.1 的待定项,由一条守卫钉住「今天是关的」——
// 所有者拍板改成开的时候,这条测试会红一次,那正是回来读 §5.1 的时刻。
func TestBypassProbeIsOffByDefaultUntilTheOwnerDecides(t *testing.T) {
	if DefaultProbeBypass {
		t.Fatal("绕过隧道的探测默认开着 —— 它会把用户真实 IP 暴露给 Anthropic/OpenAI/Google," +
			"spec §5.1 把这个决定留给所有者;真要改成默认开,连同这条守卫一起改")
	}
}

// CollectReach 自带预算,而那份预算**由这一轮真的要发几个探测派生**。
// 写死一个秒数的后果是静默的:加第五个目标的那天,最后一个目标会被外层预算
// 掐断,而被掐断的探测与「这条路不通」在报告里长得一模一样。
func TestReachBudgetGrowsWithTheNumberOfProbes(t *testing.T) {
	one := reachBudget(1)
	if one <= probeTimeout {
		t.Fatalf("一个探测的预算 %v 不比它自己的上限 %v 宽 —— 它会在探测还没到点时先到点",
			one, probeTimeout)
	}
	targets := len(leakcheck.ReachTargets())
	if got, want := reachBudget(targets), time.Duration(targets)*probeTimeout; got <= want {
		t.Fatalf("整轮预算 %v 不足以让 %d 个探测各自跑满 %v", got, targets, probeTimeout)
	}
	// 目标翻倍(bypass 那条路打开就是这个形状)时预算必须跟着长。
	if reachBudget(2*targets) <= reachBudget(targets) {
		t.Fatal("探测数翻倍而预算没长 —— 后一半会被静默掐成「没问出来」")
	}
}

// 两个拨号器都没供货时 CollectReach 返回 **nil**,不是一组 undetermined 记录。
// 「这一轮没跑探测」与「探过了、没问出来」是两句不同的话:judgeReachTarget 对
// 前者说「这一轮没有检查」,对后者说「认不出」。造一组假记录会让它说错那一句。
func TestCollectReachWithNoDialerProbesNothingAndInventsNothing(t *testing.T) {
	if got := CollectReach(context.Background(), ReachDeps{}); got != nil {
		t.Fatalf("没有拨号器时凭空造出了 %d 条记录:%+v", len(got), got)
	}
}

// 供了哪条路就只跑哪条路,而且路径名取的是导出常量。
func TestCollectReachOnlyRunsThePathsItWasGivenADialerFor(t *testing.T) {
	calls := 0
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		calls++
		return nil, errors.New("这条测试不联网")
	}
	got := CollectReach(context.Background(), ReachDeps{CurrentDial: dial})
	targets := leakcheck.ReachTargets()
	if len(got) != len(targets) || calls != len(targets) {
		t.Fatalf("记录 %d 条 / 拨号 %d 次,要各 %d —— 一个目标一条,不多不少",
			len(got), calls, len(targets))
	}
	for _, p := range got {
		if p.Path != leakcheck.ReachPathCurrent {
			t.Fatalf("只给了 current 的拨号器,却产出了 %q 那条路的记录", p.Path)
		}
	}
}

// LiveReachDeps 今天**只**供当前路径那一个拨号器。bypass 那条路没有产地是刻意的
// (spec §5.1 待所有者拍板,而且还缺一个绑物理网卡的拨号器)—— 若哪天它被接上
// 了一个**不绑网卡**的拨号器,两条路径会走同一条路,而报告仍把它们当成两条独立
// 观测并排出示:一句凭空造出来的对照,比不跑更糟。
func TestLiveReachDepsShipsNoBypassDialerWhileThePathIsOff(t *testing.T) {
	deps := LiveReachDeps()
	if deps.CurrentDial == nil {
		t.Fatal("当前路径没有拨号器 —— 这一段在真机上一个探测都不会发")
	}
	if DefaultProbeBypass == (deps.BypassDial == nil) {
		t.Fatalf("DefaultProbeBypass=%v 而 BypassDial 供货情况对不上 —— "+
			"只翻常量不供拨号器,这一轮会安静地什么都不多跑", DefaultProbeBypass)
	}
}
