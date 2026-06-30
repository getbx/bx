package cli

import (
	"path/filepath"
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
	cfg, sb, err := generateServerConfig("reality", "1.2.3.4", "www.apple.com", "", "", 0, false)
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
	cfg, sb, err := generateServerConfig("hysteria2", "1.2.3.4", "", "", "", 0, false)
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
	if _, _, err := generateServerConfig("reality", "", "", "", "", 0, false); err == nil {
		t.Error("reality 缺 host 应报错")
	}
}

func TestGenerateServerConfigBrook(t *testing.T) {
	cfg, sb, err := generateServerConfig("brook", "", "", ":9999", "mypw", 0, false)
	if err != nil || cfg.Type != "brook" || cfg.Password != "mypw" || sb != nil {
		t.Errorf("brook gen 不对: %+v sb=%v err=%v", cfg, sb != nil, err)
	}
}

func TestGenerateServerConfigRealityWithHysteria2(t *testing.T) {
	cfg, sb, err := generateServerConfig("reality", "1.2.3.4", "", "", "", 0, true)
	if err != nil {
		t.Fatalf("gen combo: %v", err)
	}
	if cfg.Type != "reality" || !strings.HasPrefix(cfg.Link, "vless://") {
		t.Errorf("主传输应 reality: %+v", cfg)
	}
	if !strings.HasPrefix(cfg.UDPLink, "hysteria2://") {
		t.Errorf("UDPLink 应 hysteria2: %q", cfg.UDPLink)
	}
	s := string(sb)
	if !strings.Contains(s, `"reality"`) || !strings.Contains(s, `"hysteria2"`) {
		t.Error("合体 sing-box 配置应同含 reality + hysteria2 入站")
	}
	// --with-hysteria2 只能配 reality
	if _, _, err := generateServerConfig("brook", "h", "", ":9999", "p", 0, true); err == nil {
		t.Error("--with-hysteria2 配非 reality 应报错")
	}
}

func TestShareChecksRealityNotFail(t *testing.T) {
	dir := t.TempDir()
	// 写一份 reality share 记录(无 Listen)
	rec := serverConfig{Type: "reality", SNI: "www.cloudflare.com", Link: "vless://u@1.2.3.4:443?security=reality&pbk=P&sid=ab&sni=www.cloudflare.com"}
	if err := writeServerConfig(filepath.Join(dir, "alice.yaml"), rec, true); err != nil {
		t.Fatal(err)
	}
	checks := shareChecks(dir)
	var found bool
	for _, c := range checks {
		if c.Name == "share.alice" {
			found = true
			if c.Status == "fail" {
				t.Errorf("reality share 不该报 fail: %+v", c)
			}
		}
	}
	if !found {
		t.Fatal("没找到 share.alice 检查项")
	}
}

func TestServerStatusSummary(t *testing.T) {
	// reality + 合体 + 多用户
	combo := serverConfig{Type: "reality", SNI: "www.cloudflare.com", Port: 443, Link: "vless://x", UDPLink: "hysteria2://y"}
	s := serverStatusSummary(combo, 3)
	for _, want := range []string{"reality", "hysteria2", "443", "www.cloudflare.com", "用户/分享: 3"} {
		if !strings.Contains(s, want) {
			t.Errorf("combo 摘要缺 %q:\n%s", want, s)
		}
	}
	// reality 纯 TCP,无用户
	tcp := serverConfig{Type: "reality", SNI: "www.apple.com", Port: 9443, Link: "vless://x"}
	s = serverStatusSummary(tcp, 0)
	if strings.Contains(s, "hysteria2") || strings.Contains(s, "用户/分享") {
		t.Errorf("纯 reality 不该显 hys2/用户: %s", s)
	}
	if !strings.Contains(s, "9443") {
		t.Errorf("应显自定义端口: %s", s)
	}
	// brook
	s = serverStatusSummary(serverConfig{Type: "brook", Listen: ":9999", Password: "p"}, 0)
	if !strings.Contains(s, "brook") || !strings.Contains(s, ":9999") {
		t.Errorf("brook 摘要不对: %s", s)
	}
}
