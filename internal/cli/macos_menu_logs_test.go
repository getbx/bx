package cli

import (
	"strings"
	"testing"
)

// 失败弹窗不再把用户指向一个 root 0600 的文件;完整原因由 Guardian 经 /v1/logs
// 发布,弹窗带「Show Details」打开日志页。四处弹窗都走同一个 showGuardianFailure。
func TestMacMenuFailureAlertsNeverPointAtRootOnlyLogs(t *testing.T) {
	main := stripSwiftComments(menuMainSwiftSource(t))
	rules := stripSwiftComments(readMenuSwiftSource(t, "RulesModel.swift"))
	for name, src := range map[string]string{"main.swift": main, "RulesModel.swift": rules} {
		if strings.Contains(src, "/var/log/bx-guard") {
			t.Fatalf("%s 里仍有指向 root 日志文件的路径 —— 普通用户打开就是 Permission denied", name)
		}
	}
	// 四处 error-only 弹窗(apps / switch server / rule group / add rule)+
	// 两处 message 弹窗(fetchRulesOnDemand / fetchServersOnDemand,措辞由
	// guardianFetchFailureInfo 按 HTTP 状态码算好)—— 六处都要走这个漏斗。
	//
	// **数的是 `self.showGuardianFailure(`,不是 `showGuardianFailure(title:`。**
	// 后者被两个**声明行**(两个重载)加上那句委托调用满足,而它认不出两处跨行写的
	// 调用(实参换行时 `title:` 不在同一行)—— 于是那个计数在「六处里只剩两处真的
	// 走漏斗」时照样够数。`self.` 前缀恰好是这六处调用的共同形状(全在
	// DispatchQueue.main.async 的闭包里),而两个声明与那句同类委托都没有它。
	if n := strings.Count(main, "self.showGuardianFailure("); n < 6 {
		t.Fatalf("self.showGuardianFailure 只有 %d 个调用方,六处失败弹窗(apps / switch server / rule group / add rule / rules fetch / servers fetch)都要走它", n)
	}
	// error-only 那个重载必须**委托**给 message 重载,不许自己再建一个 alert ——
	// 否则 Show Details 的按钮/日志高亮逻辑就有两份,容易改一处漏一处。
	errorOnlyBody, ok := swiftFunctionBody(main, "private func showGuardianFailure(title: String, error: Error)")
	if !ok {
		t.Fatal("读不出 showGuardianFailure(title:error:) 的函数体")
	}
	if !strings.Contains(errorOnlyBody, "message: error.localizedDescription, error: error)") {
		t.Fatalf("showGuardianFailure(title:error:) 必须一行转给 message: 那个真正的漏斗,不许自己再建一个 NSAlert:%s", strings.TrimSpace(errorOnlyBody))
	}
	// 真正的漏斗:说明文案由调用方传入(guardianFetchFailureInfo 按 HTTP 状态码
	// 算好的那句),但「按能力门显示 Show Details、按下后打开日志页并高亮失败码」
	// 这条判定只能有一份。
	body, ok := swiftFunctionBody(main, "private func showGuardianFailure(title: String, message: String, error: Error?)")
	if !ok {
		t.Fatal("读不出 showGuardianFailure(title:message:error:) 的函数体")
	}
	gate := strings.Index(body, "logsAvailable(capabilities: maintenanceReport?.capabilities)")
	details := strings.Index(body, `"Show Details"`)
	open := strings.Index(body, "openDiagnosticsLogs(highlighting: error.flatMap { guardianFailureCode(of: $0) })")
	if gate < 0 || details < 0 || open < 0 || gate > details || details > open {
		t.Fatalf("Show Details 必须在能力门之后出现、并把失败码交给 openDiagnosticsLogs(gate=%d details=%d open=%d)", gate, details, open)
	}
}

// fetchRulesOnDemand/fetchServersOnDemand 的措辞由 guardianFetchFailureInfo(纯函数,
// 按 HTTP 状态码算)决定,而那句文案自 2026-09-09 起承诺了一个「Show Details」按钮——
// 两个消费方必须真的走会显示这个按钮的漏斗,不能各自手搭一个只有 OK 的 NSAlert。
func TestMacMenuRulesAndServersFetchFailuresOfferShowDetails(t *testing.T) {
	main := stripSwiftComments(menuMainSwiftSource(t))
	for _, fn := range []string{
		"private func fetchRulesOnDemand(forceShow: Bool)",
		"private func fetchServersOnDemand(forceShow: Bool)",
	} {
		body, ok := swiftFunctionBody(main, fn)
		if !ok {
			t.Fatalf("读不出 %s 的函数体", fn)
		}
		if !strings.Contains(body, "self.showGuardianFailure(") {
			t.Fatalf("%s 没有走 showGuardianFailure —— 拉取失败会弹出一个只有 OK、没有 Show Details 的弹窗", fn)
		}
		if strings.Contains(body, "let alert = NSAlert()") {
			t.Fatalf("%s 仍在手搭 NSAlert —— guardianFetchFailureInfo 那句文案承诺的 Show Details 按钮不会出现", fn)
		}
	}
}

// Troubleshoot ▸ Open Logs:这一版 Guardian 支持时开日志页,不支持时退回原来的文件夹。
func TestMacMenuOpenLogsPrefersTheGuardianLogPage(t *testing.T) {
	code := menuMainSwiftCode(t)
	body, ok := swiftFunctionBody(code, "@objc private func openLogs()")
	if !ok {
		t.Fatal("读不出 openLogs 的函数体")
	}
	gate := strings.Index(body, "logsAvailable(capabilities: maintenanceReport?.capabilities)")
	page := strings.Index(body, "openDiagnosticsLogs(highlighting: nil)")
	folder := strings.Index(body, "NSWorkspace.shared.open(")
	if gate < 0 || page < 0 || folder < 0 || gate > page || page > folder {
		t.Fatalf("openLogs 要先按能力开日志页、旧版才退回文件夹(gate=%d page=%d folder=%d)", gate, page, folder)
	}
}

// 窗口只摆:高亮哪些行由纯函数 logLinesMatching 决定。
func TestMacMenuDiagnosticsWindowHighlightsByThePureJudgement(t *testing.T) {
	window := blankSwiftStringLiterals(stripSwiftComments(readMenuSwiftSource(t, "DiagnosticsWindow.swift")))
	if !strings.Contains(window, "logLinesMatching(") {
		t.Fatal("DiagnosticsWindow 没有用 logLinesMatching 决定高亮 —— 判据落进了 AppKit 那半")
	}
	if !strings.Contains(window, "onExportDiagnostics?()") {
		t.Fatal("日志页底部没有 Export Diagnostics 的出口 —— 归档那条终端路是 spec §1 表里保留的")
	}
}
