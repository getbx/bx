package config

import "testing"

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
