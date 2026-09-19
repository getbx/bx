package stats

import (
	"strings"
	"testing"

	"github.com/getbx/bx/internal/elevate"
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
		if got := HumanBytes(n); got != want {
			t.Errorf("HumanBytes(%d) = %q, want %q", n, got, want)
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
		// 隧道状态。**连着圆点一起断言**:英文里 "healthy" 是 "unhealthy" 的
		// 子串,只查前者的话「隧道挂了」那一版也照样满足这条断言。
		"● healthy",
		"72.7%",  // 代理占比 120/(120+45)
		"1.2 MB", // 上行
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Render 输出应含 %q,实际:\n%s", want, out)
		}
	}
}

func TestRender_Unhealthy(t *testing.T) {
	out := Render(Report{TunnelHealthy: false})
	if !strings.Contains(out, "○ unhealthy") {
		t.Errorf("隧道挂时应显示「不健康」,实际:\n%s", out)
	}
}

func TestRecoveryHint(t *testing.T) {
	if got := recoveryHint(Report{TunnelHealthy: true}); got != "" {
		t.Errorf("健康时 recoveryHint 应为空,实际:%q", got)
	}
	out := recoveryHint(Report{TunnelHealthy: false, Restarts: 3})
	for _, want := range []string{"kill-switch", "bx doctor", "3 reconnects", "switch to"} {
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
	for _, want := range []string{"is not running", "" + elevate.Prefix + "bx up"} {
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
	for _, want := range []string{"Via", "reality@1.2.3.4", "failover", "brook@1.2.3.4", "UDP→hysteria2@1.2.3.4"} {
		if !strings.Contains(out, want) {
			t.Errorf("status 面板缺 %q:\n%s", want, out)
		}
	}
}

func TestRenderShowsMode(t *testing.T) {
	for mode, want := range map[string]string{
		"split":         "split (China direct",
		"global":        "global (everything, China included",
		"router":        "router (only forwarded LAN traffic",
		"router-global": "allowlist",
	} {
		out := Render(Report{TunnelHealthy: true, Mode: mode})
		if !strings.Contains(out, "Mode") || !strings.Contains(out, want) {
			t.Errorf("mode=%q 面板缺 %q:\n%s", mode, want, out)
		}
	}
	// 空 mode 不显模式行(向后兼容,旧 socket 不带 mode)。
	if strings.Contains(Render(Report{TunnelHealthy: true}), "Mode") {
		t.Error("空 mode 不应显模式行")
	}
}

func TestRenderShowsWarnings(t *testing.T) {
	out := Render(Report{
		TunnelHealthy: true,
		Warnings: []Warning{{
			Name:     "packet_tunnel",
			Severity: "warn",
			Detail:   "macOS VPN service active: Work VPN",
			Hint:     "another VPN may own part of the network path",
		}},
	})
	for _, want := range []string{"Notice", "Work VPN", "another VPN"} {
		if !strings.Contains(out, want) {
			t.Errorf("warning panel missing %q:\n%s", want, out)
		}
	}
}

// 单传输(无容灾/UDP)只显当前传输,不显容灾块。
func TestRenderSingleTransportNoFailoverBlock(t *testing.T) {
	out := Render(Report{TunnelHealthy: true, Transport: "brook@1.2.3.4"})
	if strings.Contains(out, "failover") {
		t.Errorf("单传输不该显容灾:\n%s", out)
	}
	if !strings.Contains(out, "brook@1.2.3.4") {
		t.Errorf("应显当前传输:\n%s", out)
	}
}

// Via 那行不再把 Server 行刚说过的地址再印一遍 —— 但**只在真的相同时**。
func TestViaLineDropsTheAddressOnlyWhenItRepeatsTheServer(t *testing.T) {
	same := transportWithoutRedundantHost("reality@203.0.113.92", "203.0.113.92")
	if same != "reality" {
		t.Fatalf("同一台服务器时应只留传输名, got %q", same)
	}
	// **不同的时候那个地址是这一行最值钱的信息**:udp.transport 指向另一台服务器
	// 是 bx 支持的真实配置,省掉它等于把「UDP 从另一台机器出去」这件事藏起来。
	other := transportWithoutRedundantHost("hysteria2@203.0.113.7", "203.0.113.92")
	if other != "hysteria2@203.0.113.7" {
		t.Fatalf("不同服务器时必须保留地址, got %q", other)
	}
	if got := transportWithoutRedundantHost("brook", "203.0.113.92"); got != "brook" {
		t.Fatalf("没有 @ 的传输名原样返回, got %q", got)
	}
	if got := transportWithoutRedundantHost("reality@203.0.113.92", ""); got != "reality@203.0.113.92" {
		t.Fatalf("Server 未知时不许省, got %q", got)
	}

	// **断言打在渲染出来的那一行上,不只是这个纯函数上。**
	// 第一版只测了纯函数 —— 而调用点当时**根本没改**(一次脚本在写盘前抛了异常),
	// 于是一个零调用方的壳函数被一条绿测试盖着,真机输出一个字没变。
	// 那正是本仓库列过的第三种失效写法,这次是当场自己犯了一遍。
	out := Render(Report{
		Server: "203.0.113.92", SocksAddr: "127.0.0.1:1080",
		Transport: "reality@203.0.113.92", UDPTransport: "hysteria2@203.0.113.92",
		TunnelHealthy: true,
	})
	if !strings.Contains(out, "Via     reality  UDP→hysteria2") {
		t.Fatalf("Via 那一行仍在重复 Server 的地址:\n%s", out)
	}
	// 反向:地址不同时必须出现在渲染结果里。
	out = Render(Report{
		Server: "203.0.113.92", SocksAddr: "127.0.0.1:1080",
		Transport: "reality@203.0.113.92", UDPTransport: "hysteria2@203.0.113.7",
		TunnelHealthy: true,
	})
	if !strings.Contains(out, "UDP→hysteria2@203.0.113.7") {
		t.Fatalf("UDP 指向另一台服务器时必须把地址说出来:\n%s", out)
	}
}
