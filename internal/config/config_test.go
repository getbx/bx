package config

import (
	"testing"
	"time"
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
