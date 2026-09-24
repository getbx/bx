package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// **按应用窗口的环境刷新保住滚动位置**(known-gaps A8,2026-09-24 所有者真机确认:每 5 秒
// 一拍的重建会把人拽回顶部)。与 Servers / Rules 两扇窗口同一个写法、同一组判据:
// 显式打开与改搜索词从头开始(结果集变了),环境刷新与陈旧提示保住位置;位置在拆视图
// 之前取,先布局再滚。
func TestMacMenuAppTrafficWindowKeepsScrollOnAmbientRefresh(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "apps", "macos", "BxMenu", "Sources", "BxMenu", "AppTrafficWindow.swift"))
	if err != nil {
		t.Fatal(err)
	}
	window := string(src)
	for _, c := range []struct{ fn, want, why string }{
		{"func show(report:", "render(preservingScroll: false)", "显式打开也保位置 —— 第一眼看到的会是上次停的地方"},
		{"func refreshIfVisible(", "render(preservingScroll: true)", "每 5 秒一拍的刷新不保位置 —— 用户每翻到一半就被拽回顶部"},
		{"func markStaleIfVisible(", "render(preservingScroll: true)", "陈旧提示那一次重画把人拽回顶部"},
		{"func controlTextDidChange(", "render(preservingScroll: false)", "改了搜索词还停在原来的偏移上 —— 结果集已经变了"},
	} {
		body, ok := swiftFunctionBody(window, c.fn)
		if !ok {
			t.Fatalf("读不出 %s —— 守卫读不懂现在的代码了,先修它", c.fn)
		}
		if !strings.Contains(body, c.want) {
			t.Errorf("%s:%s", c.fn, c.why)
		}
	}
	render, ok := swiftFunctionBody(window, "private func render(preservingScroll: Bool)")
	if !ok {
		t.Fatal("读不出 render(preservingScroll:) —— 守卫读不懂现在的代码了,先修它")
	}
	grab := strings.Index(render, "scroll?.contentView.bounds.origin")
	tear := strings.Index(render, "view.removeFromSuperview()")
	layout := strings.Index(render, "layoutSubtreeIfNeeded()")
	restore := strings.Index(render, "contentView.scroll(to: offset)")
	if grab < 0 || tear < 0 || layout < 0 || restore < 0 {
		t.Fatal("render 里找不到取位置 / 拆视图 / 布局 / 回滚那几步 —— 守卫读不懂现在的代码了")
	}
	if grab > tear {
		t.Error("滚动位置是在拆完视图之后取的 —— 那时它已经是 0 了")
	}
	if layout > restore {
		t.Error("先滚后布局 —— 滚的是按旧内容算出来的坐标")
	}
}
