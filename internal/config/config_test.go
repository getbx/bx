package config

import (
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
