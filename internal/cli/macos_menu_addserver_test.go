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
	sw := strings.Index(body, "self.switchServer(name: target")
	if link < 0 || add < 0 || sw < 0 || link > add || add > sw {
		t.Fatalf("顺序要是 链接 → add → 用 added 切换(link=%d add=%d switch=%d)", link, add, sw)
	}
	// **target 只许从应答里的 added 来。** 它存在的唯一理由是旧 Guardian 不发那个
	// 字段(那时退回这次请求自己发出去的名字),不是「客户端再推一遍链接」——
	// 后者就是第二份判据,而两份判据迟早给出两个名字。
	if !strings.Contains(body, "let target = list.added.isEmpty ? name : list.added") {
		t.Fatal("target 不是从 list.added 派生的 —— 要么少了旧 Guardian 那条退路,要么客户端自己又推了一遍名字")
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
	// **失败码要先翻成一句话。** `code=servers_name_exists` 说的是协议;而这条路上
	// 最常见的两种失败(名字撞车、名字里有空格)恰恰是用户改一下就能过的。措辞出自
	// 纯函数(ServersModelTests 钉住内容),这里只钉「它真的接在这条路上」。
	if !strings.Contains(body, "addServerFailureMessage(") {
		t.Error("add 失败没经 addServerFailureMessage —— 用户会读到一个原始失败码")
	}
	// **这条路上的结局也不许合成一句「已添加并切换」。** 标题按服务端答的
	// `applied` 分支、正文由那个纯函数生成 —— 两行写死的字面量既不会有编译错误,
	// 也不会让任何 Swift 测试转红(那个纯函数只有它自己的单测在调),而它产生的
	// 正是这个 task 存在的理由要消灭的那句谎。确认框那条路早有同款守卫
	// (TestMacMenuNeverClaimsASwitchThatDidNotApply),两条路不许只守一条。
	if !strings.Contains(body, "outcome?.applied == true ?") {
		t.Error("标题没有按 outcome?.applied 分支 —— 切没成也会显示成「已切换」")
	}
	const outcomeCall = "addServerOutcomeMessage(added: target, switched: "
	call := strings.Index(body, outcomeCall)
	if call < 0 {
		t.Fatal("正文不是 addServerOutcomeMessage(added: list.added, switched:) 生成的 —— " +
			"那个纯函数才是「三种结局分开说」那几句话的所在")
	}
	rest := body[call+len(outcomeCall):]
	end := strings.Index(rest, ")")
	if end < 0 {
		t.Fatal("读不出 addServerOutcomeMessage 的实参 —— 守卫已经失效,先修守卫")
	}
	// **实参必须是光秃秃的一次取值。** `switched: nil` 会让三种结局塌成一种
	// (永远说「加上了但没切过去」),而它照样满足「调用了那个纯函数」。
	if arg := strings.TrimSpace(rest[:end]); arg != "outcome" {
		t.Errorf("switched: 的实参是 %q,不是那次切换真实的结局", arg)
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
