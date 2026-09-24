package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 「去掉这一台的 UDP 链接」的接线(known-gaps A5,2026-09-24)。判据在纯模型里有 Swift
// 测试;这里钉 main.swift 那一跳:勾选框由能力门控(不许写死成 true —— 旧 Guardian 会默默
// 忽略 clear_udp 而回「成功」),勾选结果真的进了请求,提示那句话读的是同一个判据。
func TestMacMenuReplaceLinkClearsUDPOnlyThroughTheCapabilityGate(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "apps", "macos", "BxMenu", "Sources", "BxMenu", "main.swift"))
	if err != nil {
		t.Fatal(err)
	}
	body, ok := swiftFunctionBody(string(src), "private func replaceServerLinkFromWindow(")
	if !ok {
		t.Fatal("读不出 replaceServerLinkFromWindow —— 守卫读不懂现在的代码了")
	}
	for _, want := range []string{
		"serverUDPClearingAvailable(capabilities: maintenanceReport?.capabilities)",
		"offersClearUDP: canClearUDP",
		"udpFieldHint(replacing: true, canClear: canClearUDP)",
		"clearUDP: links.clearUDP",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("replaceServerLinkFromWindow 里没有 %q", want)
		}
	}
	if strings.Contains(body, "offersClearUDP: true") {
		t.Error("勾选框写死成总是画 —— 旧 Guardian 上它会说「已去掉」而 UDP 链接还在")
	}
}
