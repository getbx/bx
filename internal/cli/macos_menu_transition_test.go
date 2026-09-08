package cli

import (
	"strings"
	"testing"
)

// 状态转换通知(TransitionNotice.swift)的判据有 Swift 套件守着;**接线**在
// main.swift,那是 AppKit、编不进套件 —— 与本目录其它读源码的守卫同一处境。
// 判据一律是**语义位置**,不是「文件里出现过这个词」。

// 每一轮刷新都必须把 Guardian 的应答喂给状态机,而且喂的是**这一轮的应答**
// (outcome.maintenanceReport),不是别的什么。少了这一句,通知功能与不存在在
// 输出上完全一样 —— 没有任何东西会报错。
func TestMacMenuFeedsTransitionNoticesFromEveryRefresh(t *testing.T) {
	code := menuMainSwiftCode(t)
	apply, ok := swiftFunctionBody(code, "private func applyRefresh(_ outcome: RefreshOutcome, capturedGeneration: Int)")
	if !ok {
		t.Fatal("读不出 applyRefresh 的函数体 —— 守卫已失效,先修守卫")
	}
	if !strings.Contains(apply, "observeTransition(outcome.maintenanceReport)") {
		t.Fatal("applyRefresh 没有把这一轮的 Guardian 应答喂给转换通知 —— 通知永远不会响,而界面完全正常")
	}
	observe, ok := swiftFunctionBody(code, "private func observeTransition(_ report: GuardianStatus?)")
	if !ok {
		t.Fatal("读不出 observeTransition 的函数体")
	}
	// 信号必须从应答的两个字段**原样**投影;tunnel_healthy 传别的东西
	// (`?? false` 之类)会把「没说」压成「不健康」,凭空造出一条通知。
	if !strings.Contains(observe, "protectionSignal(protectionState: report.protectionState, tunnelHealthy: report.core?.tunnelHealthy)") {
		t.Fatal("observeTransition 没有按应答的 protection_state / core.tunnel_healthy 原样投影信号")
	}
	// 状态机的答案必须被接住并投递 —— `_ = tracker.observe(…)` 与没接线一样。
	if !strings.Contains(observe, "if let notice = transitionNoticeTracker.observe(") ||
		!strings.Contains(observe, "deliverTransitionNotice(notice)") {
		t.Fatal("状态机的结果没有被投递(要 `if let notice = transitionNoticeTracker.observe(…)` 再 deliverTransitionNotice(notice))")
	}
	if strings.Contains(code, "transitionNoticeTracker.observe(.") {
		t.Fatal("有地方用字面量信号喂状态机 —— 那是没看答案就宣布状态")
	}
}

// UNUserNotificationCenter 在没打包成 bundle 的进程里一调就崩。它只许出现在
// deliverTransitionNotice 里,而那个函数第一句必须先看 notificationsAvailable,
// 后者的判据必须是 bundleIdentifier。
func TestMacMenuNotificationsRequireABundle(t *testing.T) {
	code := menuMainSwiftCode(t)
	body, ok := swiftFunctionBody(code, "private func deliverTransitionNotice(_ notice: TransitionNotice)")
	if !ok {
		t.Fatal("读不出 deliverTransitionNotice 的函数体")
	}
	gate := strings.Index(body, "notificationsAvailable")
	use := strings.Index(body, "UNUserNotificationCenter")
	if gate < 0 || use < 0 || gate > use {
		t.Fatalf("投递通知之前没有先看 notificationsAvailable(gate=%d use=%d)", gate, use)
	}
	if strings.Count(code, "UNUserNotificationCenter") != strings.Count(body, "UNUserNotificationCenter") {
		t.Fatal("UNUserNotificationCenter 在 deliverTransitionNotice 之外也被调了 —— 裸 swift run 会崩")
	}
	if !strings.Contains(code, "notificationsAvailable: Bool = Bundle.main.bundleIdentifier != nil") {
		t.Fatal("notificationsAvailable 的判据不是 Bundle.main.bundleIdentifier != nil")
	}
}

// 改完规则说「已生效」还是「要重连」,**由 Guardian 应答里的 requires_restart
// 决定**(ruleChangeFollowUp,纯函数),两个入口(规则组开关、按应用窗口右键)
// 都走同一个收尾;reconnectBx 只在 reconnectNeeded 那一支。
func TestMacMenuRuleChangeFollowUpComesFromTheServer(t *testing.T) {
	code := menuMainSwiftCode(t)
	body, ok := swiftFunctionBody(code, "private func followUpAfterRuleChange(title: String, list: RuleList)")
	if !ok {
		t.Fatal("读不出 followUpAfterRuleChange 的函数体")
	}
	if !strings.Contains(body, "ruleChangeFollowUp(requiresRestart: list.requiresRestart)") {
		t.Fatal("收尾没有按应答里的 requires_restart 判(实参必须是光秃秃的 list.requiresRestart)")
	}
	needed := strings.Index(body, "case .reconnectNeeded")
	reconnect := strings.Index(body, "reconnectBx()")
	if needed < 0 || reconnect < 0 || reconnect < needed {
		t.Fatalf("reconnectBx 必须只在 .reconnectNeeded 那一支(needed=%d reconnect=%d)", needed, reconnect)
	}
	for _, literal := range []string{"requiresRestart: true", "requiresRestart: false", "requiresRestart: nil"} {
		if strings.Contains(code, literal) {
			t.Fatalf("main.swift 里用字面量 %q 喂了收尾判据 —— 那是没看答案就下结论", literal)
		}
	}
	if n := strings.Count(code, "followUpAfterRuleChange(title:"); n < 2 {
		t.Fatalf("followUpAfterRuleChange 只有 %d 个调用方,规则组开关与按应用右键两条路要都走它", n)
	}
}

// 按应用看分流窗口的右键菜单:候选由纯模型给(appTrafficRuleMenu),菜单挂在
// 应用格上,点了经 onAddRule 回到 main.swift、经 Guardian /v1/rules 的 add 落盘。
// 每一跳都要真的接上 —— 少一跳就是「右键没反应」,而没有任何东西会报错。
func TestMacMenuAppTrafficRightClickAddsRuleThroughGuardian(t *testing.T) {
	window := menuAppTrafficWindowCode(t)
	appCell, ok := swiftFunctionBody(window, "private func appCell(_ entry: AppTrafficReport.Entry) -> NSView")
	if !ok {
		t.Fatal("读不出 appCell 的函数体")
	}
	if !strings.Contains(appCell, ".menu = contextMenu(for: entry)") {
		t.Fatal("应用格上没有挂右键菜单")
	}
	menu, ok := swiftFunctionBody(window, "private func contextMenu(for entry: AppTrafficReport.Entry) -> NSMenu?")
	if !ok {
		t.Fatal("读不出 contextMenu 的函数体")
	}
	gate := strings.Index(menu, "ruleEditingAvailable")
	items := strings.Index(menu, "appTrafficRuleMenu(dests: entry.dests)")
	if gate < 0 || items < 0 || gate > items {
		t.Fatalf("右键菜单要先看这一版 Guardian 支不支持规则编辑,再按纯模型的候选摆(gate=%d items=%d)", gate, items)
	}
	add, ok := swiftFunctionBody(window, "@objc private func addRuleItem(_ sender: NSMenuItem)")
	if !ok {
		t.Fatal("读不出 addRuleItem 的函数体")
	}
	if !strings.Contains(add, "onAddRule?(box.item.kind, box.item.pattern)") {
		t.Fatal("点了菜单项没有把纯模型给的 kind/pattern 原样交出去")
	}

	main := menuMainSwiftCode(t)
	if !strings.Contains(main, "controller.onAddRule = ") ||
		!strings.Contains(main, "addRuleFromAppTraffic(kind: kind, pattern: pattern)") {
		t.Fatal("main.swift 没有把窗口的 onAddRule 接到 addRuleFromAppTraffic")
	}
	// "add" 是字符串字面量,抹白副本上看不见 —— 这一条用只剥注释的源码。
	source := stripSwiftComments(menuMainSwiftSource(t))
	addRule, ok := swiftFunctionBody(source, "private func addRuleFromAppTraffic(kind: String, pattern: String)")
	if !ok {
		t.Fatal("读不出 addRuleFromAppTraffic 的函数体")
	}
	if !strings.Contains(addRule, `changeRule(action: "add", kind: ruleKind, pattern: pattern)`) {
		t.Fatal("addRuleFromAppTraffic 没有经 Guardian 的 changeRule(action: \"add\") 落盘")
	}
	if !strings.Contains(addRule, "followUpAfterRuleChange(title:") {
		t.Fatal("加完规则没有走统一的收尾(说已生效还是要重连)")
	}
	fetch, ok := swiftFunctionBody(main, "private func fetchAppTrafficOnDemand(forceShow: Bool)")
	if !ok {
		t.Fatal("读不出 fetchAppTrafficOnDemand 的函数体")
	}
	if !strings.Contains(fetch, "ruleEditingAvailable =\n                    rulesEditingAvailable(capabilities: self.maintenanceReport?.capabilities)") &&
		!strings.Contains(fetch, "ruleEditingAvailable = rulesEditingAvailable(capabilities: self.maintenanceReport?.capabilities)") {
		t.Fatal("窗口的 ruleEditingAvailable 没有按 Guardian 的能力清单设 —— 旧 Guardian 会挂出一个每次点都 404 的菜单")
	}
}
