package supervisor

import (
	"testing"

	"github.com/getbx/bx/internal/config"
)

// 这是本设计最危险的失败模式的唯一守卫。少一条链接 = 那台服务器的 IP 不在 bypass 里
// = 切过去之后隧道自己的流量被劫进 TUN = 成环。而成环是**静默的**:连得上、
// status 显绿,流量却在绕圈或泄漏。
func TestBypassLinksCoversEveryServerBothLinks(t *testing.T) {
	cfg := &config.Config{
		Servers: []config.Server{
			{Name: "hk", Link: "vless://u@1.1.1.1:443", UDP: "hysteria2://p@1.1.1.1:443"},
			{Name: "tokyo", Link: "vless://u@2.2.2.2:443", UDP: "hysteria2://p@3.3.3.3:443"},
			{Name: "sg", Link: "vless://u@4.4.4.4:443"}, // 没有 udp
		},
		Current: "hk",
	}
	got := map[string]bool{}
	for _, l := range bypassLinks(cfg) {
		got[l] = true
	}
	for _, want := range []string{
		"vless://u@1.1.1.1:443", "hysteria2://p@1.1.1.1:443",
		"vless://u@2.2.2.2:443", "hysteria2://p@3.3.3.3:443",
		"vless://u@4.4.4.4:443",
	} {
		if !got[want] {
			t.Errorf("bypass 漏了 %s —— 切到那台就会成环(静默:连得上、status 显绿、流量绕圈)", want)
		}
	}
}

func TestBypassLinksFallsBackToTransportsAndUDPWhenNoServerList(t *testing.T) {
	cfg := &config.Config{
		Transports: []string{"vless://u@1.1.1.1:443", "brook://x@2.2.2.2:9999"},
	}
	cfg.UDP.Transport = "hysteria2://p@3.3.3.3:443"
	got := map[string]bool{}
	for _, l := range bypassLinks(cfg) {
		got[l] = true
	}
	for _, want := range []string{"vless://u@1.1.1.1:443", "brook://x@2.2.2.2:9999", "hysteria2://p@3.3.3.3:443"} {
		if !got[want] {
			t.Errorf("旧配置路径漏了 %s", want)
		}
	}
}
