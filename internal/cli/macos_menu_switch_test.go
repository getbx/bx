package cli

import (
	"strings"
	"testing"
)

// 菜单第一行的开关(ProtectionSwitch.swift 判据 + ProtectionSwitchRow.swift 视图)。
// 判据有 Swift 套件守着;这里守 main.swift 的接线 —— 每一条缺了都不会有编译错误。

// 开关的位置只来自状态与进行中的动作;拨开接 startBx、拨关接 turnOffBx。
func TestMacMenuProtectionSwitchIsWiredToTheOriginalToggleEntries(t *testing.T) {
	code := menuMainSwiftCode(t)
	body, ok := swiftFunctionBody(code, "private func protectionSwitchRow(subtitle: String?, subtitleIsBad: Bool) -> ProtectionSwitchRow?")
	if !ok {
		t.Fatal("读不出 protectionSwitchRow 的函数体 —— 守卫已失效,先修守卫")
	}
	if !strings.Contains(body, "protectionSwitch(") ||
		!strings.Contains(body, "state: menuStateKind(), inFlight: toggleInFlight?.action)") {
		t.Fatal("开关的位置不是由 protectionSwitch(state: menuStateKind(), inFlight: toggleInFlight?.action) 判的 —— 位置必须来自状态,不来自点击")
	}
	on := strings.Index(body, "case .turnOn: self?.startBx()")
	off := strings.Index(body, "case .turnOff: self?.turnOffBx()")
	if on < 0 || off < 0 {
		t.Fatal("拨动没有接回原来的 startBx / turnOffBx —— 确认、免密、逃生口那套会被绕过")
	}
	if !strings.Contains(body, "protectionSwitchAction(turnedOn: turnedOn)") {
		t.Fatal("方向 → 动作的映射没有走纯函数 protectionSwitchAction")
	}
}

// 开关行在「有东西可拨」的每个分支里都摆了,文字版的 Turn Off / Start Protection 不再有;
// 进行中那一屏也摆(停在目标位置、禁用,进度写在它下面)。
func TestMacMenuProtectionSwitchReplacesTheTextToggleItems(t *testing.T) {
	code := menuMainSwiftCode(t)
	body, ok := swiftFunctionBody(code, "private func rebuildMenu()")
	if !ok {
		t.Fatal("读不出 rebuildMenu 的函数体")
	}
	if n := strings.Count(body, "menu.addProtectionSwitch(row)"); n < 4 {
		t.Fatalf("开关行只摆了 %d 处,要 ≥4(进行中 / connected / warning / off)", n)
	}
	for _, stray := range []string{"#selector(turnOffBx)", "#selector(startBx)"} {
		if strings.Contains(body, stray) {
			t.Fatalf("rebuildMenu 里仍有文字版的开关项 %s —— 同一个动作两个入口", stray)
		}
	}
	inFlight := strings.Index(body, "if let inFlight = toggleInFlight {")
	firstRow := strings.Index(body, "menu.addProtectionSwitch(row)")
	if inFlight < 0 || firstRow < inFlight {
		t.Fatal("进行中那一屏没有开关行(用户刚拨的地方就是他在看的地方)")
	}
	overlay := body[inFlight:firstRow]
	if !strings.Contains(overlay, "toggleProgressText(action: inFlight.action, elapsedSeconds: elapsed)") {
		t.Fatal("进行中的进度文案没有写在开关行下面")
	}
}

// 开关行看得见的一切必须进 menuSignature,否则它永远不会就地更新。
func TestMacMenuProtectionSwitchParticipatesInTheMenuSignature(t *testing.T) {
	code := menuMainSwiftCode(t)
	sig, ok := swiftFunctionBody(code, "private func menuSignature(_ menu: NSMenu) -> String")
	if !ok {
		t.Fatal("读不出 menuSignature 的函数体")
	}
	if !strings.Contains(sig, "(view as? ProtectionSwitchRow)?.signature") {
		t.Fatal("menuSignature 没有把开关行的 signature 算进去 —— 拨完之后菜单不会重画")
	}
	// 只看 `signature = [ … ].joined` 那一段:文件别处也有 `enabled ? …` 这种写法
	// (上色),整文件查会让「签名里漏了 enabled」照样绿(变异实测)。
	row := blankSwiftStringLiterals(stripSwiftComments(menuProtectionSwitchRowSource(t)))
	start := strings.Index(row, "signature = [")
	end := strings.Index(row, "].joined(")
	if start < 0 || end < start {
		t.Fatal("读不出 ProtectionSwitchRow 里 signature 的初始化表达式 —— 守卫已失效,先修守卫")
	}
	expr := row[start:end]
	for _, want := range []string{"isOn ?", "enabled ?", "subtitle ??", "subtitleIsBad ?"} {
		if !strings.Contains(expr, want) {
			t.Fatalf("ProtectionSwitchRow.signature 少算了一项(缺 %q)—— 那一项变了菜单不会重画", want)
		}
	}
}

func menuProtectionSwitchRowSource(t *testing.T) string {
	t.Helper()
	return readMenuSwiftSource(t, "ProtectionSwitchRow.swift")
}
