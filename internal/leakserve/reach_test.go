package leakserve

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
