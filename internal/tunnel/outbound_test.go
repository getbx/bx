package tunnel

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

const outboundTestVless = "vless://11111111-2222-3333-4444-555555555555@203.0.113.9:443?security=reality&sni=www.cloudflare.com&pbk=PUBKEYPUBKEYPUBKEYPUBKEYPUBKEYPUBKEYPUBKEY&sid=abcd&fp=chrome&flow=xtls-rprx-vision"

// 手机端的出站与桌面起 sing-box 用的出站必须是同一个生成器给的:两份拷贝会让「Mac 上连得上、
// 手机上连不上」变成一个两边测试都绿的漂移。钉法:桌面配置里那一条出站,除 tag 外逐键 ==
// SingboxOutbound 给的。
func TestSingboxOutboundIsTheSameOutboundTheDesktopRuns(t *testing.T) {
	v, err := parseVlessLink(outboundTestVless)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := v.singboxConfig("127.0.0.1:10800", "")
	if err != nil {
		t.Fatal(err)
	}
	var desktop struct {
		Outbounds []map[string]any `json:"outbounds"`
	}
	if err := json.Unmarshal(raw, &desktop); err != nil || len(desktop.Outbounds) != 1 {
		t.Fatalf("desktop config outbounds: %v %v", err, desktop.Outbounds)
	}
	got, err := SingboxOutbound(outboundTestVless, "proxy")
	if err != nil {
		t.Fatal(err)
	}
	if got["tag"] != "proxy" {
		t.Fatalf("tag = %v, want proxy", got["tag"])
	}
	// 走一遍 JSON,让两边的数字/嵌套 map 类型一致再比。
	var mobile map[string]any
	b, _ := json.Marshal(got)
	_ = json.Unmarshal(b, &mobile)
	delete(mobile, "tag")
	delete(desktop.Outbounds[0], "tag")
	if !reflect.DeepEqual(mobile, desktop.Outbounds[0]) {
		t.Fatalf("mobile outbound differs from desktop:\nmobile  %v\ndesktop %v", mobile, desktop.Outbounds[0])
	}
}

func TestSingboxOutboundRefusesTransportsThisPhaseDoesNotCarry(t *testing.T) {
	for _, link := range []string{"hysteria2://pw@203.0.113.9:443", "brook://server?server=203.0.113.9%3A9999&password=x", "trojan://pw@203.0.113.9:443"} {
		if _, err := SingboxOutbound(link, "proxy"); !errors.Is(err, ErrOutboundUnsupported) {
			t.Errorf("%s: err = %v, want ErrOutboundUnsupported", Kind(link), err)
		}
	}
}
