package stats

import (
	"strings"
	"testing"
)

func TestHumanBytes(t *testing.T) {
	cases := map[int64]string{
		0:          "0 B",
		512:        "512 B",
		1024:       "1.0 KB",
		1536:       "1.5 KB",
		1048576:    "1.0 MB",
		1572864:    "1.5 MB",
		1073741824: "1.0 GB",
	}
	for n, want := range cases {
		if got := humanBytes(n); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestRender_ContainsKeyInfo(t *testing.T) {
	r := Report{
		Snapshot:      Snapshot{Active: 3, Proxy: 120, Direct: 45, Blocked: 2, BytesUp: 1258291, BytesDown: 8808038},
		Server:        "203.0.113.10:9999",
		SocksAddr:     "127.0.0.1:43265",
		TunnelHealthy: true,
		LatencyMS:     42,
		Restarts:      0,
	}
	out := Render(r)

	for _, want := range []string{
		"203.0.113.10:9999", // 节点
		"42",                // 延迟
		"健康",                // 隧道状态
		"72.7%",             // 代理占比 120/(120+45)
		"1.2 MB",            // 上行
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Render 输出应含 %q,实际:\n%s", want, out)
		}
	}
}

func TestRender_Unhealthy(t *testing.T) {
	out := Render(Report{TunnelHealthy: false})
	if !strings.Contains(out, "不健康") {
		t.Errorf("隧道挂时应显示「不健康」,实际:\n%s", out)
	}
}

func TestRecoveryHint(t *testing.T) {
	if got := recoveryHint(Report{TunnelHealthy: true}); got != "" {
		t.Errorf("健康时 recoveryHint 应为空,实际:%q", got)
	}
	out := recoveryHint(Report{TunnelHealthy: false, Restarts: 3})
	for _, want := range []string{"kill-switch", "bx doctor", "重连 3", "换"} {
		if !strings.Contains(out, want) {
			t.Errorf("不健康 recoveryHint 应含 %q,实际:\n%s", want, out)
		}
	}
}

func TestRender_UnhealthyHasRecovery(t *testing.T) {
	out := Render(Report{TunnelHealthy: false, Restarts: 2})
	if !strings.Contains(out, "kill-switch") || !strings.Contains(out, "bx doctor") {
		t.Errorf("不健康面板应含恢复指引,实际:\n%s", out)
	}
	if strings.Contains(Render(Report{TunnelHealthy: true}), "kill-switch") {
		t.Error("健康面板不应含恢复块")
	}
}

func TestRenderNotRunning(t *testing.T) {
	out := RenderNotRunning()
	for _, want := range []string{"未运行", "sudo bx up"} {
		if !strings.Contains(out, want) {
			t.Errorf("RenderNotRunning 应含 %q,实际:%q", want, out)
		}
	}
}

func TestRenderShowsMultiTransport(t *testing.T) {
	out := Render(Report{
		TunnelHealthy: true,
		Transport:     "reality@1.2.3.4",
		Transports:    []string{"reality@1.2.3.4", "brook@1.2.3.4"},
		UDPTransport:  "hysteria2@1.2.3.4",
	})
	for _, want := range []string{"传输", "reality@1.2.3.4", "容灾", "brook@1.2.3.4", "UDP→hysteria2@1.2.3.4"} {
		if !strings.Contains(out, want) {
			t.Errorf("status 面板缺 %q:\n%s", want, out)
		}
	}
}

// 单传输(无容灾/UDP)只显当前传输,不显容灾块。
func TestRenderSingleTransportNoFailoverBlock(t *testing.T) {
	out := Render(Report{TunnelHealthy: true, Transport: "brook@1.2.3.4"})
	if strings.Contains(out, "容灾") {
		t.Errorf("单传输不该显容灾:\n%s", out)
	}
	if !strings.Contains(out, "brook@1.2.3.4") {
		t.Errorf("应显当前传输:\n%s", out)
	}
}
