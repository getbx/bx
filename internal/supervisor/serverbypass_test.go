package supervisor

import (
	"net/netip"
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

// 下面这两条钉的是 bypassLinks 的 Required 在**解析**环节的兑现。
// 这个分支此前一条测试都没有:它活在 run.go 的一个闭包里,要真发生一次 DNS 失败
// 才走得到。把解析抽成 resolveServerBypass 并让 host 解析可注入之后,它才可测。

// 非当前那台解析不了 = 跳过,启动照常。清单是会攒的,里面难免有退役的、
// 换了 IP 的、DNS 偶尔抖的;用一台今天根本没在用的机器堵死整台电脑的保护
// 是说不过去的。
func TestResolveServerBypassSkipsUnresolvableNonCurrentServer(t *testing.T) {
	cfg := &config.Config{
		Servers: []config.Server{
			{Name: "hk", Link: "vless://u@hk.example:443"},
			{Name: "tokyo", Link: "vless://u@tokyo.example:443"},
		},
		Current: "hk",
	}
	resolve := func(host string) []netip.Addr {
		if host == "hk.example" {
			return []netip.Addr{netip.MustParseAddr("1.1.1.1")}
		}
		return nil // tokyo 解析不了
	}
	staticA, addrs, err := resolveServerBypassWith(cfg, resolve)
	if err != nil {
		t.Fatalf("非当前那台解析不了不该连累启动: %v", err)
	}
	if _, ok := staticA["hk.example"]; !ok {
		t.Fatalf("当前那台必须在静态表里, got %v", staticA)
	}
	if _, ok := staticA["tokyo.example"]; ok {
		t.Fatalf("解析不了的那台不该进静态表(半装状态),got %v", staticA)
	}
	if len(addrs) != 1 || addrs[0] != netip.MustParseAddr("1.1.1.1") {
		t.Fatalf("只该有当前那台的地址, got %v", addrs)
	}
}

// 当前那台解析不了就必须拒绝启动:它的 IP 进不了 bypass,隧道自己连服务器的
// 流量就会被劫进 TUN —— 成环,而且是静默的(连得上、status 显绿、流量绕圈)。
func TestResolveServerBypassFailsWhenCurrentServerUnresolvable(t *testing.T) {
	cfg := &config.Config{
		Servers: []config.Server{
			{Name: "hk", Link: "vless://u@hk.example:443"},
			{Name: "tokyo", Link: "vless://u@tokyo.example:443"},
		},
		Current: "hk",
	}
	resolve := func(host string) []netip.Addr {
		if host == "tokyo.example" {
			return []netip.Addr{netip.MustParseAddr("2.2.2.2")}
		}
		return nil // 当前那台(hk)解析不了
	}
	if _, _, err := resolveServerBypassWith(cfg, resolve); err == nil {
		t.Fatal("当前那台解析不了必须报错 —— 它不在 bypass 里就是成环")
	}
}

func TestResolveServerBypassFailsWhenNothingResolves(t *testing.T) {
	cfg := &config.Config{
		Servers: []config.Server{{Name: "hk", Link: "vless://u@hk.example:443"}},
		Current: "hk",
	}
	if _, _, err := resolveServerBypassWith(cfg, func(string) []netip.Addr { return nil }); err == nil {
		t.Fatal("一个服务器 IP 都解析不出必须报错")
	}
}

func TestEqualStringSetsIgnoresOrderAndCatchesDifference(t *testing.T) {
	if !equalStringSets([]string{"1.1.1.1/32", "2.2.2.2/32"}, []string{"2.2.2.2/32", "1.1.1.1/32"}) {
		t.Fatal("顺序不同不代表集合变了 —— 会白白重装一次真实路由")
	}
	if equalStringSets([]string{"1.1.1.1/32"}, []string{"1.1.1.1/32", "2.2.2.2/32"}) {
		t.Fatal("多一条就是变了 —— 漏判就是新服务器没进 bypass = 成环")
	}
	if equalStringSets([]string{"1.1.1.1/32"}, []string{"2.2.2.2/32"}) {
		t.Fatal("换了一条就是变了")
	}
	if !equalStringSets(nil, nil) {
		t.Fatal("空对空应相等")
	}
}

// /v0/server 的请求体里就带着目标链接 —— 它不该从配置文件的 current 去**猜**自己
// 要切到哪台。spec 是「先热切、成功后再落盘 current」,故切换那一刻目标往往还不是
// current;Required 若只看文件,目标解析失败就会被当成「一台今天没在用的服务器」
// 静默跳过,然后不装 bypass 直接切过去 = 成环(连得上、status 显绿、流量绕圈)。
func TestResolveServerBypassTreatsCallerNamedLinksAsRequired(t *testing.T) {
	cfg := &config.Config{
		Servers: []config.Server{
			{Name: "hk", Link: "vless://u@hk.example:443"},
			{Name: "tokyo", Link: "vless://u@tokyo.example:443"},
		},
		Current: "hk", // 注意:文件里当前仍是 hk,目标 tokyo 还没落盘
	}
	resolve := func(host string) []netip.Addr {
		if host == "hk.example" {
			return []netip.Addr{netip.MustParseAddr("1.1.1.1")}
		}
		return nil // tokyo 解析不了
	}
	_, _, err := resolveServerBypassRequiring(cfg, []string{"vless://u@tokyo.example:443"}, resolve)
	if err == nil {
		t.Fatal("调用方点名的目标解析不了必须报错 —— 静默跳过就等于不装 bypass 切过去 = 成环")
	}
}

// 目标可能**还不在配置文件里**(先切后落盘),此时它必须仍然进 bypass 集合。
func TestResolveServerBypassIncludesCallerNamedLinkAbsentFromConfig(t *testing.T) {
	cfg := &config.Config{
		Servers: []config.Server{{Name: "hk", Link: "vless://u@hk.example:443"}},
		Current: "hk",
	}
	resolve := func(host string) []netip.Addr {
		switch host {
		case "hk.example":
			return []netip.Addr{netip.MustParseAddr("1.1.1.1")}
		case "new.example":
			return []netip.Addr{netip.MustParseAddr("9.9.9.9")}
		}
		return nil
	}
	staticA, addrs, err := resolveServerBypassRequiring(cfg, []string{"vless://u@new.example:443"}, resolve)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := staticA["new.example"]; !ok {
		t.Fatalf("配置里还没有的目标也必须进 bypass, got %v", staticA)
	}
	var found bool
	for _, a := range addrs {
		if a == netip.MustParseAddr("9.9.9.9") {
			found = true
		}
	}
	if !found {
		t.Fatalf("目标的 IP 必须进地址集合, got %v", addrs)
	}
}

// 点名只提升被点名的那些,不把整张清单变成必需 —— 否则一台退役服务器又能堵死切换了。
func TestResolveServerBypassRequiringStillSkipsOtherUnresolvableServers(t *testing.T) {
	cfg := &config.Config{
		Servers: []config.Server{
			{Name: "hk", Link: "vless://u@hk.example:443"},
			{Name: "tokyo", Link: "vless://u@tokyo.example:443"},
			{Name: "dead", Link: "vless://u@dead.example:443"}, // 退役,解析不了
		},
		Current: "hk",
	}
	resolve := func(host string) []netip.Addr {
		if host == "dead.example" {
			return nil
		}
		return []netip.Addr{netip.MustParseAddr("1.1.1.1")}
	}
	if _, _, err := resolveServerBypassRequiring(cfg, []string{"vless://u@tokyo.example:443"}, resolve); err != nil {
		t.Fatalf("没被点名的那台解析不了不该堵死切换: %v", err)
	}
}
