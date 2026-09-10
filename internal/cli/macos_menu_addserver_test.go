package cli

import (
	"strings"
	"testing"
)

// 窗口里的按钮是 Add Server…,Replace Configuration… 已从窗口退场;点了走 onAddServer。
func TestMacMenuServersWindowOffersAddServerNotReplace(t *testing.T) {
	window := stripSwiftComments(menuServersWindowSource(t))
	if !strings.Contains(window, `NSButton(title: "Add Server…"`) {
		t.Fatal("Servers 窗口没有 Add Server… 按钮")
	}
	if strings.Contains(window, "Replace Configuration") || strings.Contains(window, "onReplaceConfiguration") {
		t.Fatal("Replace Configuration 还在窗口里 —— 它被 Add Server 取代了(spec §4)")
	}
	if !strings.Contains(window, "onAddServer?()") {
		t.Fatal("Add Server… 没有回调出口")
	}
}

// 流程:贴链接 → 名字(可空)→ /v1/servers add → 用应答里的 added 切换 → 一句结果。
// 中间不弹密码、不开终端;失败走 showGuardianFailure。
func TestMacMenuAddServerAddsThenSwitchesWithoutPrivilege(t *testing.T) {
	code := menuMainSwiftCode(t)
	body, ok := swiftFunctionBody(code, "private func addServerFromWindow()")
	if !ok {
		t.Fatal("读不出 addServerFromWindow 的函数体")
	}
	link := strings.Index(body, "promptForClientLink(")
	add := strings.Index(body, "GuardianClient().addServer(name:")
	sw := strings.Index(body, "self.switchServer(name: list.added")
	if link < 0 || add < 0 || sw < 0 || link > add || add > sw {
		t.Fatalf("顺序要是 链接 → add → 用 added 切换(link=%d add=%d switch=%d)", link, add, sw)
	}
	// 这条路全程只经 owner 门的 Guardian 端点。**一个新的提权出口不会让任何
	// 既有测试转红** —— 它只会让用户在换服务器时莫名其妙地被要求输密码。
	for _, forbidden := range []string{
		"runPrivileged(", "runPrivilegedScriptOffMainThread(", "openTerminal(", "bxPath",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("Add Server 不许提权/开终端(出现了 %s)", forbidden)
		}
	}
	if !strings.Contains(body, "showGuardianFailure(title:") {
		t.Fatal("add 失败要走同一个失败漏斗")
	}
	if !strings.Contains(code, "controller.onAddServer = ") || !strings.Contains(code, "self?.addServerFromWindow()") {
		t.Fatal("窗口的 onAddServer 没接到 addServerFromWindow")
	}
	// 确认框那一半仍然只在 confirmAndSwitchServer 里;无确认的 switchServer 供 add 流复用。
	confirm, ok := swiftFunctionBody(code, "private func confirmAndSwitchServer(name: String, host: String)")
	if !ok || !strings.Contains(confirm, "switchServer(name: name") {
		t.Fatal("confirmAndSwitchServer 要经无确认的 switchServer(name:) 落地,而不是第二份切换逻辑")
	}
}
