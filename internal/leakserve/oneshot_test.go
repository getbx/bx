package leakserve

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/getbx/bx/internal/leakcheck"
	"github.com/getbx/bx/internal/tristate"
)

func postReportResponse(t *testing.T, srv *Server, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost,
		"http://"+srv.Addr().String()+"/report?t="+srv.Token(), strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", srv.Origin())
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /report: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func postReport(t *testing.T, srv *Server, body string) int {
	t.Helper()
	return postReportResponse(t, srv, body).StatusCode
}

// requirePortClosed 轮询到端口不再接受连接为止。
func requirePortClosed(t *testing.T, srv *Server, why string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", srv.Addr().String(), 200*time.Millisecond)
		if err != nil {
			return // 已经关了,符合预期
		}
		_ = conn.Close()
		if time.Now().After(deadline) {
			t.Fatal(why)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// 拿到结果即关:上报之后端口必须不再接受连接。用户关掉标签页就走人是常态,
// 留一个开着的口就是把这个新攻击面一直挂在那儿。
func TestServerClosesAfterOneReport(t *testing.T) {
	srv := newTestServer(t)
	if got := postReport(t, srv, `{"exit_v4":"5.6.7.8"}`); got != http.StatusOK {
		t.Fatalf("第一次上报应成功,得到 %d", got)
	}
	report := srv.Wait(context.Background())
	if len(report.Findings) == 0 {
		t.Fatal("Wait 应拿到那份上报判出来的报告")
	}
	requirePortClosed(t, srv, "上报之后端口仍然接受连接:一次性没有生效,这个口会一直挂着")
}

// **「拿到结果」必须是「响应已经写完」。** 关闭如果抢在响应之前,页面就拿不到
// 它要渲染的结论 —— 症状是用户点完检测看到一片空白,而服务端这边一切「正常」。
//
// 这条与上一条是两件事:上一条只要求端口关掉,它对「先关再写」照样绿。
func TestReportResponseCarriesTheFinishedReport(t *testing.T) {
	srv := newTestServer(t)
	resp := postReportResponse(t, srv, `{"exit_v4":"5.6.7.8"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("上报应成功,得到 %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		t.Fatalf("读响应体失败(一次性关闭抢在了响应之前?):%v", err)
	}
	// 刻意**不**反序列化成 leakcheck.Report:Verdict 只有 MarshalJSON,没有
	// UnmarshalJSON(页面只读那三个词,不需要反向)。照着页面看到的形状解。
	var got struct {
		Findings []struct {
			ID      string `json:"id"`
			Verdict string `json:"verdict"`
		} `json:"findings"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("响应体不是一份报告(%q):%v", raw, err)
	}
	if len(got.Findings) == 0 {
		t.Fatal("响应体里一条结论都没有:页面拿不到东西可渲染")
	}
	for _, f := range got.Findings {
		if f.ID == "" || f.Verdict == "" {
			t.Errorf("结论 %+v 缺 id 或 verdict:页面渲染不出来", f)
		}
	}
}

// 硬超时:没有任何上报时,服务必须自己关掉,而且 Wait 要返回一份
// **not checked** 的报告(设计风险四:不许把「没跑成」渲染成「一切正常」)。
func TestHardTimeoutClosesAndYieldsNotChecked(t *testing.T) {
	srv, err := Listen(Options{
		Judge: func(b leakcheck.BrowserReport) leakcheck.Report {
			return leakcheck.Judge(time.Unix(0, 0).UTC(), b, leakcheck.LocalFacts{})
		},
		HardTimeout: 120 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	go srv.Serve()

	start := time.Now()
	report := srv.Wait(context.Background())
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("Wait 应在硬超时后返回,实际等了 %s", elapsed)
	}
	if len(report.Findings) == 0 {
		t.Fatal("超时也要返回一份报告(全 not checked),不是空结构 —— " +
			"一个零值 Report 在下游读起来正是「0 处异常」")
	}
	for _, f := range report.Findings {
		if f.Verdict != leakcheck.NotChecked {
			t.Errorf("超时后 %s 必须是 not checked,得到 %s", f.ID, f.Verdict)
		}
	}
	requirePortClosed(t, srv, "硬超时之后端口仍然接受连接")
}

// **超时那条路上,本机事实那一半是真的。**
//
// 上一条喂的是 `leakcheck.LocalFacts{}`,而生产环境的 Judge 闭包里捕获的是 CLI
// 采到的真事实(internal/cli/leakcheck.go)。项目所有者的 Mac 上那份事实是:
// bx 开着 ⇒ v6 被 reject(IPv6DefaultPresent=False)、解析器 127.0.0.1。用零值
// fixture 时 IPv6 那条在 Unknown 上短路,于是这条路看起来是绿的;喂真形状,
// 它会判出一句「no IPv6 exit was observed」的 ok —— **而页面一个包都没发过。**
//
// 这条测试走的是真的 Listen/Wait,所以它同时钉住了「timedOutReport 用的是那个
// 带真事实的闭包」这件接线 —— 纯函数层的测试证明不了这一跳。
func TestHardTimeoutWithRealLocalFactsClaimsNoObservation(t *testing.T) {
	facts := leakcheck.LocalFacts{
		DefaultRouteV4:     leakcheck.InterfaceRef{Name: "utun11", Display: "bx (utun11)"},
		DefaultRouteV6:     leakcheck.InterfaceRef{Err: "no route"},
		IPv6DefaultPresent: tristate.False,
		DNSServers:         []string{"127.0.0.1"},
		DNSServerEgress:    map[string]string{"127.0.0.1": "lo0"},
		BXTunInterface:     "utun11",
		BXProtection:       "protected",
	}
	srv, err := Listen(Options{
		Judge: func(b leakcheck.BrowserReport) leakcheck.Report {
			return leakcheck.Judge(fixedNow(), b, facts)
		},
		HardTimeout: 120 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	go srv.Serve()

	report := srv.Wait(context.Background())
	// **这条守卫真正守的是措辞,不是判定** —— Critical 的原话是
	// 「no IPv6 exit was observed 断言了一次从没发生过的观测」。所以下面这一句
	// **对每一条结论都成立,与它判成什么无关**:页面一个包都没发过,就没有任何
	// 一条结论可以引用一次浏览器观测。
	for _, f := range report.Findings {
		if strings.Contains(f.Summary, "was observed") {
			t.Errorf("%s 断言了一次从没发生过的观测:%q", f.ID, f.Summary)
		}
	}
	// 判定这一半只对**本机那半答不了**的结论成立。
	//
	// DNS 与 IPv6 在这个 fixture 上都由本机事实独立答得了(解析器在本机;
	// IPv6DefaultPresent=False ⇒ 没有通往 v6 互联网的路 ⇒ 漏不出去),
	// 把它们压成 not checked 是朝反方向撒谎 —— 明明查过。
	settledLocally := map[string]bool{leakcheck.FindingDNS: true, leakcheck.FindingIPv6: true}
	for _, f := range report.Findings {
		if settledLocally[f.ID] {
			continue
		}
		if f.Verdict != leakcheck.NotChecked {
			t.Errorf("页面一个包都没发过,%s 必须是 not checked,得到 %s(summary=%q)",
				f.ID, f.Verdict, f.Summary)
		}
	}
	// 而 IPv6 那条在这个 fixture 上必须给出**本机可支撑的 ok**:
	// 变成 not checked 就说明那条本地判据又被浏览器缺席拖下水了。
	for _, f := range report.Findings {
		if f.ID == leakcheck.FindingIPv6 && f.Verdict != leakcheck.OK {
			t.Errorf("确知没有 v6 通路时应给出本机可支撑的 ok,得到 %s(%q)", f.Verdict, f.Summary)
		}
	}
}

// ctx 取消与硬超时同理:调用方撤了,口也得关,而且返回的仍是 not checked。
func TestCanceledWaitClosesAndYieldsNotChecked(t *testing.T) {
	srv := newTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	report := srv.Wait(ctx)
	if len(report.Findings) == 0 {
		t.Fatal("取消也要返回一份 not checked 的报告,不是空结构")
	}
	for _, f := range report.Findings {
		if f.Verdict != leakcheck.NotChecked {
			t.Errorf("取消后 %s 必须是 not checked,得到 %s", f.ID, f.Verdict)
		}
	}
	requirePortClosed(t, srv, "Wait 被取消之后端口仍然接受连接")
}

// 2 分钟这个数字是设计定的,钉住它。改它要先改这条测试 —— 一个被悄悄改成
// 半小时的「硬超时」,与没有硬超时的区别只是慢一点。
func TestDefaultHardTimeoutIsTwoMinutes(t *testing.T) {
	if DefaultHardTimeout != 2*time.Minute {
		t.Fatalf("硬超时应为 2 分钟,得到 %s", DefaultHardTimeout)
	}
	srv, err := Listen(Options{Judge: func(leakcheck.BrowserReport) leakcheck.Report {
		return leakcheck.Report{}
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	if srv.hardTimeout != DefaultHardTimeout {
		t.Fatalf("HardTimeout 留空时应回落到 %s,得到 %s", DefaultHardTimeout, srv.hardTimeout)
	}
}

// Close 可以被调用多次(defer 一次、超时路径一次、上报路径一次),不许 panic。
func TestCloseIsIdempotent(t *testing.T) {
	srv := newTestServer(t)
	_ = srv.Close()
	_ = srv.Close()
}
