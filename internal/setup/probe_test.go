package setup

import (
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

// 非法 vless 链接(缺 pbk)早失败,不静默放过。
func TestBuildProbeTunnelRealityRejectsBadLink(t *testing.T) {
	dir := t.TempDir()
	link := "vless://uuid@1.2.3.4:9998?security=reality&sni=www.apple.com" // 缺 pbk/sid
	if _, _, err := buildProbeTunnel(dir, link, "1.1.1.1:443"); err == nil {
		t.Fatal("非法 vless 链接应报错")
	}
}
