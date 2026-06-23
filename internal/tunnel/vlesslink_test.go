package tunnel

import "testing"

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
