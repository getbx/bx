package setup

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

// vless:// 链接必须走 reality 分支(内嵌 sing-box),不能误用 brook —— 修的就是这个 bug。
func TestBuildProbeTunnelRealitySelectsSingbox(t *testing.T) {
	dir := t.TempDir()
	link := "vless://be625ca6-947d-46f4-8567-4bdcc5fd530d@1.2.3.4:9998?security=reality&pbk=PUB&sid=ab12&sni=www.apple.com"
	// 仅构造不 Start:Stop() 会阻塞在未关闭的 done(无 goroutine 关它),故直接丢弃隧道。
	_, cleanup, err := buildProbeTunnel(dir, link, "1.1.1.1:443")
	if err != nil {
		t.Fatalf("buildProbeTunnel reality: %v", err)
	}
	defer cleanup()
	if _, err := os.Stat(filepath.Join(dir, "sing-box")); err != nil {
		t.Errorf("reality 分支应落盘内嵌 sing-box: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "brook")); err == nil {
		t.Error("reality 分支不该落盘 brook(误用了 brook 引擎)")
	}
}

// brook:// 链接仍走 brook 分支,不碰 sing-box。
func TestBuildProbeTunnelBrookSelectsBrook(t *testing.T) {
	dir := t.TempDir()
	link := "brook://server?server=1.2.3.4%3A9999&password=pw"
	_, cleanup, err := buildProbeTunnel(dir, link, "1.1.1.1:443")
	if err != nil {
		t.Fatalf("buildProbeTunnel brook: %v", err)
	}
	defer cleanup()
	if _, err := os.Stat(filepath.Join(dir, "brook")); err != nil {
		t.Errorf("brook 分支应落盘内嵌 brook: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "sing-box")); err == nil {
		t.Error("brook 分支不该落盘 sing-box")
	}
}

// 回归守卫:hysteria2/trojan/ss/vmess 链接都必须走内嵌 sing-box,绝不误回落 brook
// (修的正是这个 bug —— 这四种此前在 setup 探测里被当成 brook,导致 bx setup 误报连不通)。
func TestBuildProbeTunnelNonBrookSelectsSingbox(t *testing.T) {
	ssUserinfo := base64.RawURLEncoding.EncodeToString([]byte("aes-256-gcm:pw123"))
	vmessJSON := base64.StdEncoding.EncodeToString([]byte(`{"add":"1.2.3.4","port":"443","id":"uuid-x","net":"tcp"}`))
	cases := map[string]string{
		"hysteria2": "hysteria2://pw@1.2.3.4:8443?sni=bing.com",
		"trojan":    "trojan://pw@1.2.3.4:443?sni=bing.com",
		"ss":        "ss://" + ssUserinfo + "@1.2.3.4:8388#hk",
		"vmess":     "vmess://" + vmessJSON,
	}
	for name, link := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			_, cleanup, err := buildProbeTunnel(dir, link, "1.1.1.1:443")
			if err != nil {
				t.Fatalf("buildProbeTunnel %s: %v", name, err)
			}
			defer cleanup()
			if _, err := os.Stat(filepath.Join(dir, "sing-box")); err != nil {
				t.Errorf("%s 应落盘内嵌 sing-box: %v", name, err)
			}
			if _, err := os.Stat(filepath.Join(dir, "brook")); err == nil {
				t.Errorf("%s 不该落盘 brook(误用 brook 引擎=误报连不通)", name)
			}
		})
	}
}

// 非法 vless 链接(缺 pbk)早失败,不静默放过。
func TestBuildProbeTunnelRealityRejectsBadLink(t *testing.T) {
	dir := t.TempDir()
	link := "vless://uuid@1.2.3.4:9998?security=reality&sni=www.apple.com" // 缺 pbk/sid
	if _, _, err := buildProbeTunnel(dir, link, "1.1.1.1:443"); err == nil {
		t.Fatal("非法 vless 链接应报错")
	}
}
