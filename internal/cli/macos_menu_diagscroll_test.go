package cli

import (
	"strings"
	"testing"
)

// **每一次重画都必须回到顶部。**
//
// 2026-09-10 真机:打开 Checks 页,第一眼看到的是最末尾那几行 OK —— 合计句、
// 版本行、以及唯一那条 WARN 全在屏幕外面。排序把最严重的放在最前,而
// NSScrollView 重画之后停在原来的偏移上,于是一个以「坏的排前」为卖点的页面,
// 第一眼给的恰好是最不重要的那一端。同一件事还让 Run again 看起来没反应:
// 重画完画面停在同一个位置,而健康机器上两份报告逐字相同。
//
// 判据钉在**两页各自的渲染函数体**里,而不是「文件里出现过 scrollToTop」——
// 后者在「只有 Checks 页调、Logs 页忘了」时照样绿。
func TestMacMenuDiagnosticsPagesReturnToTheTopAfterRendering(t *testing.T) {
	window := blankSwiftStringLiterals(stripSwiftComments(readMenuSwiftSource(t, "DiagnosticsWindow.swift")))
	for _, tc := range []struct{ fn, scroll string }{
		{"private func renderChecks(_ report: DoctorReport)", "scrollToTop(checksScroll)"},
		{"private func renderLogs(_ report: LogsReport, code: String?)", "scrollToTop(logsScroll)"},
	} {
		body, ok := swiftFunctionBody(window, tc.fn)
		if !ok {
			t.Fatalf("读不出 %s 的函数体 —— 守卫已失效,先修守卫", tc.fn)
		}
		if !strings.Contains(body, tc.scroll) {
			t.Errorf("%s 渲染完没有 %s:重画之后停在旧的滚动位置,"+
				"最严重的那几行会被挡在屏幕外面", tc.fn, tc.scroll)
		}
	}

	// 滚到顶靠的是三件事一起:先让布局落定(否则文档视图还是上一次的高度,
	// 滚到的是按旧内容算出来的坐标)、滚到 .zero(文档视图是 FlippedView,
	// 顶部就是零点)、再让 scroll view 认这次改动。少哪一件都会静默失效。
	body, ok := swiftFunctionBody(window, "private func scrollToTop(_ scroll: NSScrollView?)")
	if !ok {
		t.Fatal("读不出 scrollToTop 的函数体")
	}
	for _, want := range []string{"layoutSubtreeIfNeeded()", "contentView.scroll(to: .zero)", "reflectScrolledClipView("} {
		if !strings.Contains(body, want) {
			t.Errorf("scrollToTop 缺 %s", want)
		}
	}
}

// Run again 之后数据常常与上一次逐字相同,页面重画完看起来毫无变化 —— 真机上
// 所有者连点两次、以为按钮坏了。那一行时间戳是「刚才那一下确实发生过」的唯一
// 证据,而它必须出自纯函数:窗口自己拼一次格式,就没有任何测试盯着它了。
func TestMacMenuChecksPageShowsWhenItLastRan(t *testing.T) {
	window := blankSwiftStringLiterals(stripSwiftComments(readMenuSwiftSource(t, "DiagnosticsWindow.swift")))
	body, ok := swiftFunctionBody(window, "private func renderChecks(_ report: DoctorReport)")
	if !ok {
		t.Fatal("读不出 renderChecks 的函数体")
	}
	if !strings.Contains(body, "doctorCheckedAtLine(Date())") {
		t.Error("Checks 页没有摆上「上次检查」的时间 —— Run again 之后用户无从知道它跑过")
	}
}
