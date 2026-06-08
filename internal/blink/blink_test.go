package blink

import "testing"

func TestEncodeDecodeRoundTrip(t *testing.T) {
	link := "brook://server?server=1.2.3.4%3A9999&password=pw"
	enc := Encode(link)
	if enc[:8] != "blink://" {
		t.Fatalf("应以 blink:// 开头, got %q", enc)
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
		t.Fatal("非 blink scheme 应报错")
	}
}

func TestDecodeRejectsBadBase64(t *testing.T) {
	if _, err := Decode("blink://!!!not-base64!!!"); err == nil {
		t.Fatal("坏 base64 应报错")
	}
}

func TestDecodeRejectsNonBrookContent(t *testing.T) {
	bad := Encode("http://evil")
	if _, err := Decode(bad); err == nil {
		t.Fatal("解出非 brook 内容应报错")
	}
}
