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
		got[l.Link] = true
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
	// Parse 会把空的 udp.mode 补成 "proxy";这里直接构造结构体,故手动补上,
	// 否则测的是一份生产中不存在的配置。
	cfg.UDP.Mode = "proxy"
	cfg.UDP.Transport = "hysteria2://p@3.3.3.3:443"
	got := map[string]bool{}
	for _, l := range bypassLinks(cfg) {
		got[l.Link] = true
	}
	for _, want := range []string{"vless://u@1.1.1.1:443", "brook://x@2.2.2.2:9999", "hysteria2://p@3.3.3.3:443"} {
		if !got[want] {
			t.Errorf("旧配置路径漏了 %s", want)
		}
	}
}

// 清单是会攒的:加过的服务器一直留着,其中难免有退役的、换了 IP 的、DNS 偶尔抖的。
// 让其中任意一台解析失败就堵死整台机器的保护,等于用「一台我今天根本没在用的
// 服务器」勒索用户。而当前那台不一样 —— 它进不了 bypass 就是成环。
func TestOnlyCurrentServerLinksAreRequired(t *testing.T) {
	cfg := &config.Config{
		Servers: []config.Server{
			{Name: "hk", Link: "vless://u@1.1.1.1:443", UDP: "hysteria2://p@1.1.1.1:443"},
			{Name: "tokyo", Link: "vless://u@2.2.2.2:443", UDP: "hysteria2://p@3.3.3.3:443"},
		},
		Current: "hk",
	}
	required := map[string]bool{}
	for _, l := range bypassLinks(cfg) {
		required[l.Link] = l.Required
	}
	for _, want := range []string{"vless://u@1.1.1.1:443", "hysteria2://p@1.1.1.1:443"} {
		if !required[want] {
			t.Errorf("当前那台的 %s 必须是 Required —— 它进不了 bypass 就是成环(静默)", want)
		}
	}
	for _, want := range []string{"vless://u@2.2.2.2:443", "hysteria2://p@3.3.3.3:443"} {
		if required[want] {
			t.Errorf("非当前的 %s 不该是 Required —— 一台没在用的服务器解析不了不该堵死启动", want)
		}
	}
}

// 旧配置(没有 servers 清单)保持原样:那是一份为自动容灾精挑的短表,
// 里面每一条都随时可能被自动切上去,故全部必需。
func TestLegacyTransportsAreAllRequired(t *testing.T) {
	cfg := &config.Config{Transports: []string{"vless://u@1.1.1.1:443", "brook://x@2.2.2.2:9999"}}
	cfg.UDP.Transport = "hysteria2://p@3.3.3.3:443"
	cfg.UDP.Mode = "proxy"
	for _, l := range bypassLinks(cfg) {
		if !l.Required {
			t.Errorf("旧路径的 %s 必须是 Required", l.Link)
		}
	}
}

// udp.mode != proxy 时 udp.transport 根本不会被挂载,旧代码也就不给它做 bypass。
// 本轮重构必须原样保住 —— 旧配置的行为一个字节都不该变。
func TestLegacyUDPBypassStillGatedByProxyMode(t *testing.T) {
	cfg := &config.Config{Transports: []string{"vless://u@1.1.1.1:443"}}
	cfg.UDP.Transport = "hysteria2://p@3.3.3.3:443"
	cfg.UDP.Mode = "block"
	for _, l := range bypassLinks(cfg) {
		if l.Link == cfg.UDP.Transport {
			t.Fatal("mode != proxy 时 udp.transport 不该进 bypass(它根本不会被挂载)")
		}
	}
}
