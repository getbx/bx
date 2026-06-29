package blink

import (
	"encoding/base64"
	"testing"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	link := "brook://server?server=1.2.3.4%3A9999&password=pw"
	enc := Encode(link)
	if enc[:5] != "bx://" {
		t.Fatalf("应以 bx:// 开头, got %q", enc)
	}
	dec, err := Decode(enc)
	if err != nil {
		t.Fatal(err)
	}
	if dec != link {
		t.Fatalf("round-trip 不一致: %q != %q", dec, link)
	}
}

func TestDecodeRejectsWrongScheme(t *testing.T) {
	if _, err := Decode("brook://x"); err == nil {
		t.Fatal("非 bx scheme 应报错")
	}
}

func TestDecodeRejectsBadBase64(t *testing.T) {
	if _, err := Decode("bx://!!!not-base64!!!"); err == nil {
		t.Fatal("坏 base64 应报错")
	}
}

func TestDecodeRejectsNonBrookContent(t *testing.T) {
	bad := Encode("http://evil")
	if _, err := Decode(bad); err == nil {
		t.Fatal("解出不支持内容应报错")
	}
}

func TestDecodeAcceptsLegacyBlink(t *testing.T) {
	link := "brook://server?server=1.2.3.4%3A9999&password=pw"
	legacy := "blink://" + base64.RawURLEncoding.EncodeToString([]byte(link))
	dec, err := Decode(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if dec != link {
		t.Fatalf("legacy round-trip = %q, want %q", dec, link)
	}
}

func TestDecodeAcceptsLegacyRawBX(t *testing.T) {
	link := "brook://server?server=1.2.3.4%3A9999&password=pw"
	legacy := "bx://" + base64.RawURLEncoding.EncodeToString([]byte(link))
	dec, err := Decode(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if dec != link {
		t.Fatalf("legacy bx round-trip = %q, want %q", dec, link)
	}
}

func TestDecodeRejectsUnsupportedTransport(t *testing.T) {
	raw := []byte(`{"v":1,"transport":"future","link":"future://x"}`)
	link := "bx://" + base64.RawURLEncoding.EncodeToString(raw)
	if _, err := Decode(link); err == nil {
		t.Fatal("unsupported transport should fail")
	}
}

func TestEncodeDecodeVlessRoundTrip(t *testing.T) {
	link := "vless://be625ca6@1.2.3.4:9998?security=reality&pbk=PUB&sid=ab12&sni=www.apple.com&flow=xtls-rprx-vision&fp=chrome"
	enc := Encode(link)
	if enc[:5] != "bx://" {
		t.Fatalf("应以 bx:// 开头, got %q", enc)
	}
	dec, err := Decode(enc)
	if err != nil {
		t.Fatalf("vless 换壳 round-trip 应成功: %v", err)
	}
	if dec != link {
		t.Fatalf("round-trip 不一致: %q != %q", dec, link)
	}
}
