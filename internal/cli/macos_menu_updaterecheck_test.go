package cli

import (
	"strings"
	"testing"
)

// known-gaps A12:打开菜单时补查更新。判据 shouldRecheckUpdateOnMenuOpen 在 Swift 套件里有
// 单测;这里钉接线(main.swift 编不进测试 target):menuWillOpen 真的问了判据、喂的是真状态,
// 而 refreshUpdateCheck 真的记下尝试时刻、并在答案落定后清掉在飞标志 —— 少了后者,第一次
// 补查之后在飞标志永远是 true,补查从此再也不会发生,两边测试照样全绿。
func TestMacMenuRechecksUpdatesWhenOpenedAfterAnHour(t *testing.T) {
	src := stripSwiftComments(menuMainSwiftSource(t))
	open, ok := swiftFunctionBody(src, "func menuWillOpen(_ menu: NSMenu) {")
	if !ok {
		t.Fatal("main.swift 里找不到 menuWillOpen —— 锚点漂了")
	}
	for _, want := range []string{
		"shouldRecheckUpdateOnMenuOpen(lastAttempt: updateCheckLastAttempt, now: Date(), inFlight: updateCheckInFlight)",
		"refreshUpdateCheck()",
	} {
		if !strings.Contains(open, want) {
			t.Errorf("menuWillOpen 缺 %s", want)
		}
	}
	check, ok := swiftFunctionBody(src, "private func refreshUpdateCheck() {")
	if !ok {
		t.Fatal("main.swift 里找不到 refreshUpdateCheck")
	}
	for _, want := range []string{"updateCheckLastAttempt = Date()", "updateCheckInFlight = true", "self.updateCheckInFlight = false"} {
		if !strings.Contains(check, want) {
			t.Errorf("refreshUpdateCheck 缺 %s", want)
		}
	}
	if strings.Index(check, "self.updateCheckInFlight = false") < strings.Index(check, "DispatchQueue.main.async") {
		t.Error("在飞标志必须在答案落回主线程之后才清,否则两次检查会叠起来")
	}
}
