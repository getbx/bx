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

func TestEncodeMultiRoundTrip(t *testing.T) {
	links := []string{
		"vless://u@1.2.3.4:9998?security=reality&pbk=K&sid=ab&sni=www.apple.com",
		"brook://server?server=1.2.3.4%3A9999&password=pw",
	}
	enc := EncodeMulti(links)
	if enc[:5] != "bx://" {
		t.Fatalf("应以 bx:// 开头: %q", enc)
	}
	got, err := DecodeAll(enc)
	if err != nil {
		t.Fatalf("DecodeAll: %v", err)
	}
	if len(got) != 2 || got[0] != links[0] || got[1] != links[1] {
		t.Fatalf("round-trip 不一致: %v", got)
	}
}

// 单元素 bundle 退化为 legacy 单格式(向后兼容:旧 Decode 也能读)。
func TestEncodeMultiSingleIsCompat(t *testing.T) {
	link := "brook://server?server=1.2.3.4%3A9999&password=pw"
	enc := EncodeMulti([]string{link})
	dec, err := Decode(enc) // 旧单链接 Decode 仍能读
	if err != nil || dec != link {
		t.Fatalf("单元素应兼容旧 Decode: %q err=%v", dec, err)
	}
}

// Decode(bundle) 返回首条(主传输);DecodeAll 返回全部。
func TestDecodeReturnsFirstOfBundle(t *testing.T) {
	links := []string{
		"vless://u@1.2.3.4:9998?security=reality&pbk=K&sid=ab&sni=www.apple.com",
		"brook://server?server=1.2.3.4%3A9999&password=pw",
	}
	first, err := Decode(EncodeMulti(links))
	if err != nil || first != links[0] {
		t.Fatalf("Decode 应返回首条: %q err=%v", first, err)
	}
}

// DecodeAll 对单格式/legacy 也返回 1 元素列表。
func TestDecodeAllSingleEnvelope(t *testing.T) {
	link := "vless://u@h:1?security=reality&pbk=K&sid=a&sni=s"
	got, err := DecodeAll(Encode(link))
	if err != nil || len(got) != 1 || got[0] != link {
		t.Fatalf("单 envelope DecodeAll = %v err=%v", got, err)
	}
}

// bundle 里夹带不受支持内容 → 拒绝(内容闸不放松)。
func TestDecodeAllRejectsBadLinkInBundle(t *testing.T) {
	if _, err := DecodeAll(EncodeMulti([]string{"brook://ok", "http://evil"})); err == nil {
		t.Fatal("bundle 含不支持链接应报错")
	}
}
