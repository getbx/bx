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
	}, "current")
	if got.State != leakcheck.ReachReachable {
		t.Fatalf("State = %v, want reachable —— 405 + 服务 JSON 是最强的可达证据", got.State)
	}
	if got.Path != "current" {
		t.Fatalf("Path = %q, want current", got.Path)
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
	}, "bypass")
	if got.State != leakcheck.ReachUnreachable {
		t.Fatalf("State = %v, want unreachable", got.State)
	}
	if strings.Contains(got.Detail, "/private/var/secret") {
		t.Fatalf("Detail 带出了原始错误:%q", got.Detail)
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
