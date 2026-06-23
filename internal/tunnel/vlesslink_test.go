package tunnel

import (
	"encoding/json"
	"testing"
)

func TestParseVlessLink(t *testing.T) {
	s := "vless://11111111-2222-3333-4444-555555555555@203.0.113.10:443" +
		"?security=reality&pbk=PUBKEYxyz&sid=abcd1234&sni=www.microsoft.com" +
		"&flow=xtls-rprx-vision&fp=chrome&type=tcp#mudi"
	v, err := parseVlessLink(s)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if v.UUID != "11111111-2222-3333-4444-555555555555" {
		t.Errorf("uuid=%q", v.UUID)
	}
	if v.Host != "203.0.113.10" || v.Port != 443 {
		t.Errorf("host:port=%s:%d", v.Host, v.Port)
	}
	if v.PublicKey != "PUBKEYxyz" || v.ShortID != "abcd1234" {
		t.Errorf("pbk=%q sid=%q", v.PublicKey, v.ShortID)
	}
	if v.SNI != "www.microsoft.com" || v.Flow != "xtls-rprx-vision" || v.Fingerprint != "chrome" {
		t.Errorf("sni=%q flow=%q fp=%q", v.SNI, v.Flow, v.Fingerprint)
	}
}

func TestParseVlessLinkErrors(t *testing.T) {
	cases := map[string]string{
		"not vless":   "brook://server?server=1.2.3.4%3A9999",
		"no uuid":     "vless://@1.2.3.4:443?security=reality&pbk=x&sid=y&sni=z",
		"not reality": "vless://uid@1.2.3.4:443?security=tls&sni=z",
		"missing pbk": "vless://uid@1.2.3.4:443?security=reality&sid=y&sni=z",
		"missing sni": "vless://uid@1.2.3.4:443?security=reality&pbk=x&sid=y",
		"bad port":    "vless://uid@1.2.3.4:notaport?security=reality&pbk=x&sid=y&sni=z",
	}
	for name, s := range cases {
		if _, err := parseVlessLink(s); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
}

func TestSingboxConfig(t *testing.T) {
	v := vlessLink{
		UUID: "uid", Host: "203.0.113.10", Port: 443,
		PublicKey: "PBK", ShortID: "SID", SNI: "www.microsoft.com",
		Flow: "xtls-rprx-vision", Fingerprint: "chrome",
	}
	b, err := v.singboxConfig("127.0.0.1:10800")
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatalf("not valid json: %v", err)
	}
	in := cfg["inbounds"].([]any)[0].(map[string]any)
	if in["type"] != "socks" || in["listen"] != "127.0.0.1" || in["listen_port"].(float64) != 10800 {
		t.Errorf("inbound wrong: %v", in)
	}
	out := cfg["outbounds"].([]any)[0].(map[string]any)
	if out["type"] != "vless" || out["server"] != "203.0.113.10" || out["server_port"].(float64) != 443 {
		t.Errorf("outbound wrong: %v", out)
	}
	tls := out["tls"].(map[string]any)
	reality := tls["reality"].(map[string]any)
	if tls["server_name"] != "www.microsoft.com" || reality["public_key"] != "PBK" || reality["short_id"] != "SID" {
		t.Errorf("tls/reality wrong: %v", tls)
	}
}
