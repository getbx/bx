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
	if n := strings.Count(main, "showGuardianFailure(title:"); n < 4 {
		t.Fatalf("showGuardianFailure 只有 %d 个调用方,四处失败弹窗(apps / switch server / rule group / add rule)都要走它", n)
	}
	body, ok := swiftFunctionBody(main, "private func showGuardianFailure(title: String, error: Error)")
	if !ok {
		t.Fatal("读不出 showGuardianFailure 的函数体")
	}
	gate := strings.Index(body, "logsAvailable(capabilities: maintenanceReport?.capabilities)")
	details := strings.Index(body, `"Show Details"`)
	open := strings.Index(body, "openDiagnosticsLogs(highlighting: guardianFailureCode(of: error))")
	if gate < 0 || details < 0 || open < 0 || gate > details || details > open {
		t.Fatalf("Show Details 必须在能力门之后出现、并把失败码交给 openDiagnosticsLogs(gate=%d details=%d open=%d)", gate, details, open)
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
