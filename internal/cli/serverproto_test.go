package cli

import (
	"strings"
	"testing"
)

func TestNormalizeServerProtocol(t *testing.T) {
	for in, want := range map[string]string{"": "brook", "brook": "brook", "reality": "reality", "hysteria2": "hysteria2"} {
		if got, err := normalizeServerProtocol(in); err != nil || got != want {
			t.Errorf("normalizeServerProtocol(%q)=%q,%v want %q", in, got, err, want)
		}
	}
	if _, err := normalizeServerProtocol("vmess"); err == nil {
		t.Error("不支持的协议应报错")
	}
}

func TestServerConfigComplete(t *testing.T) {
	if serverConfigComplete(serverConfig{Type: "brook", Listen: ":9999", Password: "p"}) != nil {
		t.Error("完整 brook 应通过")
	}
	if serverConfigComplete(serverConfig{Type: "brook"}) == nil {
		t.Error("brook 缺 listen/password 应报错")
	}
	if serverConfigComplete(serverConfig{Type: "reality", Link: "vless://x"}) != nil {
		t.Error("有 link 的 reality 应通过")
	}
	if serverConfigComplete(serverConfig{Type: "reality"}) == nil {
		t.Error("reality 缺 link 应报错")
	}
}

func TestGenerateServerConfigReality(t *testing.T) {
	cfg, sb, err := generateServerConfig("reality", "1.2.3.4", "www.apple.com", "", "", 0)
	if err != nil {
		t.Fatalf("gen: %v", err)
	}
	if cfg.Type != "reality" || cfg.SNI != "www.apple.com" {
		t.Errorf("cfg 不对: %+v", cfg)
	}
	if !strings.HasPrefix(cfg.Link, "vless://") || !strings.Contains(cfg.Link, "security=reality") {
		t.Errorf("link 不对: %q", cfg.Link)
	}
	if !strings.Contains(string(sb), `"reality"`) {
		t.Error("sing-box 配置应含 reality")
	}
}

func TestGenerateServerConfigHysteria2(t *testing.T) {
	cfg, sb, err := generateServerConfig("hysteria2", "1.2.3.4", "", "", "", 0)
	if err != nil {
		t.Fatalf("gen: %v", err)
	}
	if cfg.Type != "hysteria2" || !strings.HasPrefix(cfg.Link, "hysteria2://") {
		t.Errorf("cfg 不对: %+v", cfg)
	}
	if !strings.Contains(string(sb), "salamander") {
		t.Error("sing-box 配置应含 salamander obfs")
	}
}

func TestGenerateServerConfigRealityNeedsHost(t *testing.T) {
	if _, _, err := generateServerConfig("reality", "", "", "", "", 0); err == nil {
		t.Error("reality 缺 host 应报错")
	}
}

func TestGenerateServerConfigBrook(t *testing.T) {
	cfg, sb, err := generateServerConfig("brook", "", "", ":9999", "mypw", 0)
	if err != nil || cfg.Type != "brook" || cfg.Password != "mypw" || sb != nil {
		t.Errorf("brook gen 不对: %+v sb=%v err=%v", cfg, sb != nil, err)
	}
}
