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
