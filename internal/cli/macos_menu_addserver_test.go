package cli

import (
	"strings"
	"testing"
)

// 窗口里的按钮是 Add Server…,Replace Configuration… 已从窗口退场;点了走 onAddServer。
func TestMacMenuServersWindowOffersAddServerNotReplace(t *testing.T) {
	window := stripSwiftComments(menuServersWindowSource(t))
	// **判据钉的是「有这条出口」,不是某一个措辞。** 标签 2026-09-18 改过一次:
	// 「New Server…」与「Add Server…」并排时名字近义而动作完全不同(一个 ssh 进
	// 空 VPS 装 bx server,一个只是把已有链接加进清单),点错前者的代价是对着一台
	// 陌生机器跑 ssh。所以这里只要求「装新机器」与「加已有」各有一个按钮、且两个
	// 标题不许再撞在一起。
	deploy, okDeploy := swiftButtonTitleFor(window, "deployServer")
	add, okAdd := swiftButtonTitleFor(window, "addServer")
	if !okDeploy || !okAdd {
		t.Fatalf("Servers 窗口缺按钮:deploy=%v add=%v —— 守卫读不懂现在的代码了", okDeploy, okAdd)
	}
	if deploy == add {
		t.Fatalf("两个按钮同名 %q", deploy)
	}
	// 两个标题都提到 server/VPS 却互相区分不开,正是这次要消灭的东西:
	// 必须一个说「新装一台」、一个说「加一条已有的」。
	if !strings.Contains(strings.ToLower(deploy), "new") {
		t.Fatalf("装新机器那个按钮没说它是「新」的:%q", deploy)
	}
	if !strings.Contains(strings.ToLower(add), "existing") {
		t.Fatalf("加已有那个按钮没说它是「已有」的:%q —— 与「装一台新的」区分不开", add)
	}
	if strings.Contains(window, "Replace Configuration") || strings.Contains(window, "onReplaceConfiguration") {
		t.Fatal("Replace Configuration 还在窗口里 —— 它被 Add Server 取代了(spec §4)")
	}
	if !strings.Contains(window, "onAddServer?()") {
		t.Fatal("「加一条已有的」没有回调出口")
	}
}

// swiftButtonTitleFor 取 `NSButton(title: "…", target: self, action: #selector(<sel>))` 的标题。
func swiftButtonTitleFor(src, selector string) (string, bool) {
	for _, line := range strings.Split(src, "\n") {
		if !strings.Contains(line, "#selector("+selector+")") {
			continue
		}
		const marker = `NSButton(title: "`
		i := strings.Index(line, marker)
		if i < 0 {
			continue
		}
		rest := line[i+len(marker):]
		j := strings.Index(rest, `"`)
		if j < 0 {
			continue
		}
		return rest[:j], true
	}
	return "", false
}

// 流程:贴链接 → 名字(可空)→ /v1/servers add → 用应答里的 added 切换 → 一句结果。
// 中间不弹密码、不开终端;失败走 showGuardianFailure。
func TestMacMenuAddServerAddsThenSwitchesWithoutPrivilege(t *testing.T) {
	code := menuMainSwiftCode(t)
	body, ok := swiftFunctionBody(code, "private func addServerFromWindow()")
	if !ok {
		t.Fatal("读不出 addServerFromWindow 的函数体")
	}
	link := strings.Index(body, "promptForServerLinks(")
	add := strings.Index(body, "GuardianClient().addServer(name:")
	sw := strings.Index(body, "self.switchServer(name: target")
	if link < 0 || add < 0 || sw < 0 || link > add || add > sw {
		t.Fatalf("顺序要是 链接 → add → 用 added 切换(link=%d add=%d switch=%d)", link, add, sw)
	}
	// **UDP 那条链接要真的发出去。** `bx server install` 默认就给两条,而此前
	// 这个表单只有一个框、客户端也从不发这个参数 —— 从菜单加一台
	// reality+hysteria2 的 VPS 会静默丢掉 QUIC 那半,`bx status` 上看不出来。
	if !strings.Contains(body, "udp: links.udp") {
		t.Error("Add 表单收了 UDP 链接却没发出去 —— QUIC 那半被静默丢掉")
	}
	// 「留空」在 add 与 replace 上不是同一件事,措辞由纯函数按路给。
	if !strings.Contains(body, "udpHint: udpFieldHint(replacing: false)") {
		t.Error("Add 表单没有按自己那条路取 UDP 提示 —— 会告诉用户留空是「保持不变」")
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
