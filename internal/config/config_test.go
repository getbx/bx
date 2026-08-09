package config

import (
	"strings"
	"testing"
	"time"

	"github.com/getbx/bx/internal/blink"
)

func TestLoadValid(t *testing.T) {
	yaml := []byte(`
server: "brook://abc"
killswitch: true
dns:
  china: 223.5.5.5
  fakeip_cidr: 198.18.0.0/15
rules:
  - direct: ["*.internal.com", "10.0.0.0/8"]
  - proxy: ["*.openai.com"]
lists:
  china_domain: /tmp/china_domain.txt
  china_cidr: /tmp/china_cidr4.txt
`)
	c, err := Parse(yaml)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if c.Server != "brook://abc" || !c.Killswitch {
		t.Fatalf("bad scalar fields: %+v", c)
	}
	if c.DNS.China != "223.5.5.5" || c.DNS.FakeipCIDR != "198.18.0.0/15" {
		t.Fatalf("bad dns: %+v", c.DNS)
	}
	if len(c.Rules) != 2 || c.Rules[0].Direct[0] != "*.internal.com" {
		t.Fatalf("bad rules: %+v", c.Rules)
	}
}

func TestParseRejectsEmptyServer(t *testing.T) {
	if _, err := Parse([]byte(`killswitch: true`)); err == nil {
		t.Fatal("expected error for missing server")
	}
}

func TestParseDecodesBXServerLink(t *testing.T) {
	raw := "brook://server?server=1.2.3.4%3A9999&password=pw"
	c, err := Parse([]byte("server: \"" + blink.Encode(raw) + "\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Server != raw {
		t.Fatalf("server = %q, want %q", c.Server, raw)
	}
}

func TestParseDefaultsForBootstrap(t *testing.T) {
	c, err := Parse([]byte("server: \"brook://x\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.DataDir != "/var/lib/bx" {
		t.Errorf("DataDir 默认应为 /var/lib/bx, got %q", c.DataDir)
	}
	if !c.Lists.AutoUpdateEnabled() {
		t.Error("AutoUpdate 默认应为 true")
	}
	if c.Lists.RefreshInterval() != 24*time.Hour {
		t.Errorf("Interval 默认应为 24h, got %v", c.Lists.RefreshInterval())
	}
	if c.UDP.Mode != "proxy" {
		t.Errorf("UDP mode 默认应为 proxy, got %q", c.UDP.Mode)
	}
}

func TestParseUDPMode(t *testing.T) {
	c, err := Parse([]byte("server: \"brook://x\"\nudp:\n  mode: direct-realtime\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.UDP.Mode != "direct-realtime" {
		t.Fatalf("UDP mode = %q, want direct-realtime", c.UDP.Mode)
	}
}

func TestParseRejectsBadUDPMode(t *testing.T) {
	if _, err := Parse([]byte("server: \"brook://x\"\nudp:\n  mode: leak-everything\n")); err == nil {
		t.Fatal("expected error for invalid udp mode")
	}
}

func TestParseListsOverrides(t *testing.T) {
	c, err := Parse([]byte("server: \"brook://x\"\nlists:\n  auto_update: false\n  interval: 1h\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Lists.AutoUpdateEnabled() {
		t.Error("auto_update:false 应禁用")
	}
	if c.Lists.RefreshInterval() != time.Hour {
		t.Errorf("interval:1h 应解析为 1h, got %v", c.Lists.RefreshInterval())
	}
}

func TestParseSplitDNS(t *testing.T) {
	c, err := Parse([]byte(`
server: "brook://abc"
dns:
  split:
    - domains: ["*.shanghai-electric.com"]
      server: 10.0.13.23
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(c.DNS.Split) != 1 {
		t.Fatalf("want 1 split rule, got %d", len(c.DNS.Split))
	}
	r := c.DNS.Split[0]
	if len(r.Domains) != 1 || r.Domains[0] != "*.shanghai-electric.com" {
		t.Fatalf("bad domains: %+v", r.Domains)
	}
	if r.Server != "10.0.13.23:53" { // 无端口时补 :53
		t.Fatalf("want server 10.0.13.23:53, got %q", r.Server)
	}
}

func TestParseRejectsUnknownField(t *testing.T) {
	// 严格模式:未知字段必须报错(就是 dns.split 这次该报而没报的根因)。
	_, err := Parse([]byte(`
server: "brook://abc"
totally_unknown_field: 1
`))
	if err == nil {
		t.Fatal("expected error for unknown field, got nil")
	}
}

func TestParseRejectsSplitMissingServer(t *testing.T) {
	_, err := Parse([]byte(`
server: "brook://abc"
dns:
  split:
    - domains: ["*.x.com"]
`))
	if err == nil {
		t.Fatal("expected error for split rule without server")
	}
}

func TestParseSplitServerTrailingColon(t *testing.T) {
	c, err := Parse([]byte("server: \"brook://abc\"\ndns:\n  split:\n    - domains: [\"*.x.com\"]\n      server: \"10.0.13.23:\"\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if c.DNS.Split[0].Server != "10.0.13.23:53" {
		t.Fatalf("trailing colon should normalize to :53, got %q", c.DNS.Split[0].Server)
	}
}

func TestParseSplitServerKeepsExplicitPort(t *testing.T) {
	c, err := Parse([]byte("server: \"brook://abc\"\ndns:\n  split:\n    - domains: [\"*.x.com\"]\n      server: \"10.0.13.23:5353\"\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if c.DNS.Split[0].Server != "10.0.13.23:5353" {
		t.Fatalf("explicit port must be preserved, got %q", c.DNS.Split[0].Server)
	}
}

func TestParseRejectsSplitEmptyDomains(t *testing.T) {
	_, err := Parse([]byte("server: \"brook://abc\"\ndns:\n  split:\n    - server: 10.0.13.23\n"))
	if err == nil {
		t.Fatal("expected error for empty domains list")
	}
}

func TestParseRouterMode(t *testing.T) {
	c, err := Parse([]byte(`
server: "brook://abc"
mode: router
router:
  lan_cidrs:
    - 192.168.8.0/24
    - 10.20.0.0/24
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if c.Mode != "router" {
		t.Fatalf("mode = %q, want router", c.Mode)
	}
	if len(c.Router.LANCIDRs) != 2 || c.Router.LANCIDRs[0] != "192.168.8.0/24" {
		t.Fatalf("bad lan_cidrs: %+v", c.Router.LANCIDRs)
	}
}

func TestModeDefaultsHost(t *testing.T) {
	c, err := Parse([]byte(`server: "brook://abc"`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if c.Mode != "host" {
		t.Fatalf("default mode = %q, want host", c.Mode)
	}
}

func TestParseRejectsBadMode(t *testing.T) {
	if _, err := Parse([]byte("server: \"brook://abc\"\nmode: bogus\n")); err == nil {
		t.Fatal("expected error for bad mode")
	}
}

func TestParseRejectsBadLANCIDR(t *testing.T) {
	_, err := Parse([]byte(`
server: "brook://abc"
mode: router
router:
  lan_cidrs: ["not-a-cidr"]
`))
	if err == nil {
		t.Fatal("expected error for bad lan_cidr")
	}
}

func TestRouterModeWithoutCIDRsOK(t *testing.T) {
	// empty lan_cidrs is allowed (auto-detect at hijack time)
	c, err := Parse([]byte("server: \"brook://abc\"\nmode: router\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if c.Mode != "router" || len(c.Router.LANCIDRs) != 0 {
		t.Fatalf("bad: %+v", c)
	}
}

func TestFakeipFilterDefault(t *testing.T) {
	c, err := Parse([]byte(`server: "brook://abc"`))
	if err != nil {
		t.Fatal(err)
	}
	// sensible defaults: local/reverse domains and Tailscale control/MagicDNS
	// domains never get a fake-IP.
	want := map[string]bool{
		"*.lan": true, "*.local": true, "*.localdomain": true, "*.arpa": true,
		"tailscale.com": true, "ts.net": true,
	}
	if len(c.DNS.FakeipFilter) != len(want) {
		t.Fatalf("default fakeip_filter = %v", c.DNS.FakeipFilter)
	}
	for _, d := range c.DNS.FakeipFilter {
		if !want[d] {
			t.Fatalf("unexpected default filter entry %q (%v)", d, c.DNS.FakeipFilter)
		}
	}
}

func TestFakeipFilterCustom(t *testing.T) {
	c, err := Parse([]byte(`
server: "brook://abc"
dns:
  fakeip_filter: ["*.ts.net", "*.corp"]
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.DNS.FakeipFilter) != 2 || c.DNS.FakeipFilter[0] != "*.ts.net" {
		t.Fatalf("custom fakeip_filter = %v", c.DNS.FakeipFilter)
	}
}

func TestParseSingboxFields(t *testing.T) {
	y := []byte("server: vless://uid@1.2.3.4:443?security=reality&pbk=p&sid=s&sni=www.microsoft.com\n" +
		"singbox_url: https://vps.example.com/dl/sing-box-arm64\n" +
		"singbox_sha256: abcdef\n")
	c, err := Parse(y)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if c.SingboxURL != "https://vps.example.com/dl/sing-box-arm64" || c.SingboxSHA256 != "abcdef" {
		t.Fatalf("singbox fields: url=%q sha=%q", c.SingboxURL, c.SingboxSHA256)
	}
}

func TestParseTransportsMulti(t *testing.T) {
	c, err := Parse([]byte("transports:\n  - brook://server?server=1.2.3.4%3A9999&password=pw\n  - vless://uuid@1.2.3.4:9998?security=reality&pbk=K&sid=ab&sni=www.apple.com\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(c.Transports) != 2 {
		t.Fatalf("应有 2 个传输, got %d", len(c.Transports))
	}
	if c.Server != c.Transports[0] {
		t.Fatalf("Server 应=首条传输, server=%q t0=%q", c.Server, c.Transports[0])
	}
	if c.Transports[0][:8] != "brook://" || c.Transports[1][:8] != "vless://" {
		t.Fatalf("传输顺序/内容不对: %v", c.Transports)
	}
}

func TestParseSingleServerBecomesTransports(t *testing.T) {
	c, err := Parse([]byte("server: brook://server?server=1.2.3.4%3A9999&password=pw\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(c.Transports) != 1 || c.Transports[0] != c.Server {
		t.Fatalf("单 server 应成 1 元素 transports: %v server=%q", c.Transports, c.Server)
	}
}

func TestParseNeitherServerNorTransports(t *testing.T) {
	if _, err := Parse([]byte("killswitch: true\n")); err == nil {
		t.Fatal("server 和 transports 都空应报错")
	}
}

func TestParseUDPTransportDecodes(t *testing.T) {
	c, err := Parse([]byte("server: brook://x\nudp:\n  mode: proxy\n  transport: hysteria2://pw@1.2.3.4:8443?sni=bing.com\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if c.UDP.Transport != "hysteria2://pw@1.2.3.4:8443?sni=bing.com" {
		t.Fatalf("udp.transport = %q", c.UDP.Transport)
	}
}

func TestParseUDPTransportRequiresProxyMode(t *testing.T) {
	// udp.transport 配了但 mode=block → 报错(不静默失效)
	_, err := Parse([]byte("server: brook://x\nudp:\n  mode: block\n  transport: hysteria2://pw@h:443\n"))
	if err == nil {
		t.Fatal("udp.transport + mode!=proxy 应报错")
	}
}

func TestParseHostsOverrides(t *testing.T) {
	cfg, err := Parse([]byte(`
server: brook://example.com:9999
hosts:
  Torchfun.com.: 127.0.0.1
  api.example.com: 192.168.50.10
`))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	got, err := cfg.HostOverrides()
	if err != nil {
		t.Fatalf("HostOverrides: %v", err)
	}
	// 归一化:小写 + 去尾点。Torchfun.com. 与 torchfun.com 必须是同一条,
	// 否则用户按其中一种写法配、DNS 按另一种查,覆盖静默不生效。
	if a, ok := got["torchfun.com"]; !ok || a.String() != "127.0.0.1" {
		t.Fatalf("torchfun.com = %v ok=%v", a, ok)
	}
	if a, ok := got["api.example.com"]; !ok || a.String() != "192.168.50.10" {
		t.Fatalf("api.example.com = %v ok=%v", a, ok)
	}
	if len(got) != 2 {
		t.Fatalf("条目数 = %d, want 2", len(got))
	}
}

// 非法值必须在解析期就拒绝启动。
//
// staticA 是 DNS 第一跳且不受 rules 影响 —— 一个运行期被静默丢弃的覆盖,
// 用户会以为它生效了,而这正是本功能要消灭的那种困惑。
func TestParseRejectsBadHostOverrides(t *testing.T) {
	for _, tt := range []struct{ name, value string }{
		{"域名而非 IP", "another.example.com"},
		{"IPv6", "::1"},
		{"空值", ""},
		{"未指定地址", "0.0.0.0"},
		{"带端口", "127.0.0.1:53"},
		{"CIDR", "127.0.0.0/8"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte("server: brook://example.com:9999\nhosts:\n  bad.example.com: \"" + tt.value + "\"\n"))
			if err == nil {
				t.Fatalf("值 %q 必须在解析期报错", tt.value)
			}
			if !strings.Contains(err.Error(), "bad.example.com") {
				t.Fatalf("错误必须点名是哪条 hosts 出问题,实际 = %v", err)
			}
		})
	}
}

// 空域名同样是配置错误,不能默默跳过。
func TestParseRejectsEmptyHostKey(t *testing.T) {
	if _, err := Parse([]byte("server: brook://example.com:9999\nhosts:\n  \"\": 127.0.0.1\n")); err == nil {
		t.Fatal("空域名必须报错")
	}
}

// 没配 hosts 时不得凭空造出条目。
func TestParseWithoutHostsYieldsNone(t *testing.T) {
	cfg, err := Parse([]byte("server: brook://example.com:9999\n"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := cfg.HostOverrides()
	if err != nil {
		t.Fatalf("HostOverrides: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("未配 hosts 时应为空,实际 %v", got)
	}
}

// HostOverrides 不能只对经过 Parse 的 Config 生效。测试代码里到处用字面量
// 直接构造 config.Config{...}(如 internal/supervisor/router_test.go),这条
// 路径绕开 Parse —— 若 HostOverrides 读的是一个只由 Parse 填充的缓存字段,
// 这里就会静默拿到 nil,尽管 Hosts 明明配了合法值。
func TestHostOverridesWorksOnLiteralConfigNotOnlyParse(t *testing.T) {
	cfg := &Config{Hosts: map[string]string{"literal.example.com": "10.0.0.9"}}
	got, err := cfg.HostOverrides()
	if err != nil {
		t.Fatalf("HostOverrides: %v", err)
	}
	if a, ok := got["literal.example.com"]; !ok || a.String() != "10.0.0.9" {
		t.Fatalf("literal.example.com = %v ok=%v, want 10.0.0.9 present", a, ok)
	}
}

// 反过来,字面量构造出的非法 Hosts 必须响亮地失败(返回 error),而不是让
// HostOverrides 假装"没有任何覆盖"——那样会把同一个"配了但没生效"的陷阱
// 从用户的 YAML 搬到调用方代码里。Parse 是校验用户输入的关口;绕开它手造
// 非法 Config 属于调用方的编程错误。
//
// 这里刻意不用 panic:bx Core 是被 Guardian 监管的常驻进程,panic 会被当
// 异常退出重新拉起,而新进程带着同一份非法 Config 会在同一处立刻再 panic
// 一次——响亮的失败变成了静默的崩溃循环。error 能让调用方把它变成一次
// 干净的启动失败。
func TestHostOverridesReturnsErrorOnInvalidLiteralHosts(t *testing.T) {
	cfg := &Config{Hosts: map[string]string{"bad.example.com": "not-an-ip"}}
	got, err := cfg.HostOverrides()
	if err == nil {
		t.Fatal("非法 Hosts 应返回 error,而不是静默返回空/残缺结果")
	}
	if got != nil {
		t.Fatalf("出错时不应返回非 nil 结果,实际 %v", got)
	}
	if !strings.Contains(err.Error(), "bad.example.com") {
		t.Fatalf("错误必须点名是哪条 hosts 出问题,实际 = %v", err)
	}
}

// 归一化后的重复必须报错,而不是让后写入的值悄悄覆盖先写入的值——那正是
// 「按其中一种写法配、按另一种查」这个风险的另一面:两条写法都配了,却只有
// 一条生效,用户毫无办法从配置文件看出会是哪一条。
func TestParseRejectsNormalizedDuplicateHosts(t *testing.T) {
	_, err := Parse([]byte("server: brook://example.com:9999\nhosts:\n  Torchfun.com.: 127.0.0.1\n  torchfun.com: 127.0.0.2\n"))
	if err == nil {
		t.Fatal("归一化后重复的 hosts 必须报错")
	}
	if !strings.Contains(err.Error(), "torchfun.com") {
		t.Fatalf("错误必须点名冲突的域名,实际 = %v", err)
	}
}

// NormalizeHostName 必须去掉全部尾点,而不是只去一个——否则 "example.com.."
// 归一化成 "example.com." 而非 "example.com",被当成一条独立的合法条目放行,
// 是「畸形输入被当作合法值容忍」这条原则在归一化步骤上的翻版。
func TestNormalizeHostNameStripsAllTrailingDots(t *testing.T) {
	cfg, err := Parse([]byte("server: brook://example.com:9999\nhosts:\n  example.com..: 127.0.0.1\n"))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	got, err := cfg.HostOverrides()
	if err != nil {
		t.Fatalf("HostOverrides: %v", err)
	}
	if a, ok := got["example.com"]; !ok || a.String() != "127.0.0.1" {
		t.Fatalf("example.com = %v ok=%v, want normalized entry under example.com", a, ok)
	}
	if _, ok := got["example.com."]; ok {
		t.Fatal("不应残留带尾点的独立条目")
	}
}

// 仅由点号组成的域名去尾点后是空字符串,必须落进「域名不能为空」的既有校验,
// 而不是被当成合法 key 放行。
func TestParseRejectsDotsOnlyHostKey(t *testing.T) {
	if _, err := Parse([]byte("server: brook://example.com:9999\nhosts:\n  \"..\": 127.0.0.1\n")); err == nil {
		t.Fatal("仅有点号的域名必须报错")
	}
}
