package cli

import (
	"reflect"
	"sort"
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
// **另一半判据是「只有这一处调它、而它自己只被那两处调」。** `/v1/doctor` 会让
// Guardian 在隧道外面探测一次服务器、还要 shell 出去跑 launchctl —— 那是一次带外网
// 出口的动作,只能由用户点击触发(菜单项与窗口里的 Run again,两者都汇进本函数)。
// 混进 refresh/applyRefresh/watch 循环/任何定时器,就是每隔几秒替用户发一次隧道外的
// 探测,而界面上完全看不出来。
//
// **两半缺一不可,而只做被调那一半正是本守卫的第一版栽的地方**:只数
// `fetchDoctor()` 的出现次数,把 `openDiagnosticsChecks()` 加进 `applyRefresh` 时
// 计数仍是 1、区间判定仍然满足、三条守卫全绿 —— 而后果恰恰就是上面那句注释描述的
// 那件事。**一条声称守着某条纪律、而实际守不住的注释,比没有注释更糟**:下一个人读到
// 它就不再去检查那件事了。故:
//
//	下游 —— 整份 main.swift 里 `fetchDoctor()` 恰好一次,且落在本函数体的跨度内;
//	上游 —— `openDiagnosticsChecks()` 恰好两个调用点(定义行不算),一个在
//	        `runDoctorFromMenu` 的函数体里,一个在 `diagnosticsWindow` 那个 lazy
//	        初始化闭包里(Run again 的接线),别处一个都不许有。
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

	// 上游那一半:谁在调 openDiagnosticsChecks。定义行(`func openDiagnosticsChecks()`)
	// 不算调用。
	defs := swiftFunctionDefs(code)
	lazyStart, lazyEnd, ok := swiftLazyInitializerSpan(code, "private lazy var diagnosticsWindow: DiagnosticsWindowController = ")
	if !ok {
		t.Fatal("读不出 diagnosticsWindow 那个 lazy 初始化闭包的跨度 —— 守卫读不懂现在的 main.swift 了,请连它一起重写")
	}
	var callers []string
	for offset := 0; ; {
		at := strings.Index(code[offset:], "openDiagnosticsChecks()")
		if at < 0 {
			break
		}
		at += offset
		offset = at + 1
		if strings.HasSuffix(code[:at], "func ") {
			continue // 定义行
		}
		if at >= lazyStart && at < lazyEnd {
			callers = append(callers, "diagnosticsWindow(lazy)")
			continue
		}
		if fn := enclosingSwiftFunc(defs, at); fn != "" {
			callers = append(callers, fn)
			continue
		}
		// 落在任何函数体与那个闭包之外(类级属性初始化、顶层闭包……)。守卫读不懂的
		// 位置一律**响亮失败**,不静默放行 —— 这个文件在这上面栽过五次。
		callers = append(callers, "<不在任何已知跨度内>")
	}
	want := []string{"diagnosticsWindow(lazy)", "runDoctorFromMenu"}
	sort.Strings(callers)
	if !reflect.DeepEqual(callers, want) {
		t.Fatalf("openDiagnosticsChecks 的调用点应当恰好是 %v,实际是 %v —— 它让 Guardian 在隧道外探测一次服务器,只许由用户点击那两条路触发", want, callers)
	}
}

// swiftLazyInitializerSpan 返回 `private lazy var x: T = { … }()` 那个闭包体在
// `code` 里的字节区间(不含两端花括号)。`code` 须是抹白过的副本(注释与字符串
// 字面量里的花括号不参与配平),偏移逐字节不变,故区间对原串同样成立。
func swiftLazyInitializerSpan(code, decl string) (int, int, bool) {
	start := strings.Index(code, decl)
	if start < 0 {
		return 0, 0, false
	}
	open := strings.Index(code[start:], "{")
	if open < 0 {
		return 0, 0, false
	}
	open += start
	depth := 0
	for i := open; i < len(code); i++ {
		switch code[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return open + 1, i, true
			}
		}
	}
	return 0, 0, false
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
