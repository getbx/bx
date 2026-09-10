package cli

import (
	"strings"
	"testing"
)

// 「Check for Problems」在这一版 Guardian 有 doctor 能力时开 Checks 页,旧版才退回
// 终端那条路(exportDiagnostics)。顺序判据:能力门在前,两条路都在。
func TestMacMenuCheckForProblemsPrefersTheDoctorPage(t *testing.T) {
	code := menuMainSwiftCode(t)
	body, ok := swiftFunctionBody(code, "@objc private func runDoctorFromMenu()")
	if !ok {
		t.Fatal("读不出 runDoctorFromMenu 的函数体 —— 守卫已失效,先修守卫")
	}
	gate := strings.Index(body, "doctorAvailable(capabilities: maintenanceReport?.capabilities)")
	page := strings.Index(body, "openDiagnosticsChecks()")
	terminal := strings.Index(body, "exportDiagnostics()")
	if gate < 0 || page < 0 || terminal < 0 || gate > page || page > terminal {
		t.Fatalf("要先按能力开 Checks 页、旧版才退回终端(gate=%d page=%d terminal=%d)", gate, page, terminal)
	}
}

// Checks 页由 fetchDoctor 喂,结果经 showChecks 摆;拉不到就明说,不摆空页。
//
// **另一半判据是「只有这一处调它」。** `/v1/doctor` 会让 Guardian 在隧道外面探测一次
// 服务器、还要 shell 出去跑 launchctl —— 那是一次带外网出口的动作,只能由用户点击
// 触发(菜单项与窗口里的 Run again,两者都汇进本函数)。混进 refresh/applyRefresh/
// watch 循环/任何定时器,就是每隔几秒替用户发一次隧道外的探测,而界面上完全看不出来。
// 故这里数的是**整份 main.swift 里 `fetchDoctor()` 的出现次数**,且那一处必须落在本
// 函数体的跨度内。
func TestMacMenuDoctorPageIsFedByFetchDoctor(t *testing.T) {
	code := menuMainSwiftCode(t)
	body, ok := swiftFunctionBody(code, "private func openDiagnosticsChecks()")
	if !ok {
		t.Fatal("读不出 openDiagnosticsChecks 的函数体")
	}
	for _, want := range []string{"diagnosticsFetchInFlight", "GuardianClient().fetchDoctor()", "self.diagnosticsWindow.showChecks(report)", "showMessage("} {
		if !strings.Contains(body, want) {
			t.Fatalf("openDiagnosticsChecks 缺 %s", want)
		}
	}
	if !strings.Contains(code, "controller.onRunAgain = ") || !strings.Contains(code, "self?.openDiagnosticsChecks()") {
		t.Fatal("窗口的 Run again 没接回 openDiagnosticsChecks")
	}
	// 出现次数必须恰好一次,且落在 openDiagnosticsChecks 的函数体里。用函数体在
	// 整份源码里的**字节区间**判定归属:`swiftFunctionBody` 返回的是原串的切片,
	// 而 `menuMainSwiftCode` 已经把注释与字符串字面量抹白、偏移逐字节不变,所以
	// 这两个下标可以直接比。
	if n := strings.Count(code, "fetchDoctor()"); n != 1 {
		t.Fatalf("main.swift 里 fetchDoctor() 出现 %d 次 —— 它让 Guardian 在隧道外探测一次服务器,只许由用户点击那一条路调用", n)
	}
	at := strings.Index(code, "fetchDoctor()")
	from := strings.Index(code, body)
	if from < 0 || at < from || at >= from+len(body) {
		t.Fatalf("唯一那次 fetchDoctor() 不在 openDiagnosticsChecks 的函数体里(at=%d body=[%d,%d))", at, from, from+len(body))
	}
}

// 窗口只摆:排序、合计、标题全由纯模型给。
func TestMacMenuDiagnosticsWindowRendersChecksByThePureModel(t *testing.T) {
	window := blankSwiftStringLiterals(stripSwiftComments(readMenuSwiftSource(t, "DiagnosticsWindow.swift")))
	body, ok := swiftFunctionBody(window, "private func renderChecks(_ report: DoctorReport)")
	if !ok {
		t.Fatal("读不出 renderChecks 的函数体")
	}
	for _, want := range []string{"sortedDoctorChecks(report.checks)", "doctorSummaryLine(report.checks)", "doctorCheckTitle(", "onRunAgain?()"} {
		if !strings.Contains(body, want) && !strings.Contains(window, want) {
			t.Fatalf("Checks 页缺 %s —— 判据落进了 AppKit 那半,或 Run again 没出口", want)
		}
	}
	if !strings.Contains(window, "NSTabView") {
		t.Fatal("两页要用 NSTabView(Checks / Logs),不要两个窗口")
	}
}
