package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 2026-09-08 菜单精简(约 18 行 → 11 行)的接线守卫。判据全部是**语义位置**。
// 「怎么压缩」的判据在 MenuRows.swift 的 compactMenuRows(Swift 套件守着);
// 这里守的是 rebuildMenu 真的用了它、子菜单真的装了那三项、搬走的两项真的到了
// 服务器窗口 —— 每一处少了都不会有编译错误,界面只是多一行/少一扇门。

// 已连接状态只摆压缩后的行。把 `compactMenuRows(menuRowsNow())` 改回
// `menuRowsNow().rows`,五行诊断值就全回来了,而没有任何东西会红。
func TestMacMenuConnectedStateRendersCompactRows(t *testing.T) {
	body, ok := swiftFunctionBody(menuMainSwiftCode(t), "private func rebuildMenu()")
	if !ok {
		t.Fatal("读不出 rebuildMenu 的函数体 —— 守卫已失效,先修守卫")
	}
	if !strings.Contains(body, "for row in compactMenuRows(menuRowsNow())") {
		t.Fatal("已连接状态没有走 compactMenuRows —— 五行诊断值又常驻回来了")
	}
	if strings.Contains(body, "menuRowsNow().rows") {
		t.Fatal("rebuildMenu 里还有地方直接摆完整集合 menuRowsNow().rows")
	}
}

// Troubleshoot 子菜单:Check for Problems / Open Logs / Uninstall 都在里面,
// 且**不再**出现在一级菜单;子菜单经 addSubmenu 挂进主菜单。
func TestMacMenuTroubleshootSubmenuHoldsTheRareActions(t *testing.T) {
	source := stripSwiftComments(menuMainSwiftSource(t))
	body, ok := swiftFunctionBody(source, "private func rebuildMenu()")
	if !ok {
		t.Fatal("读不出 rebuildMenu 的函数体")
	}
	switchAt := strings.Index(body, "switch state {")
	if switchAt < 0 {
		t.Fatal("找不到状态 switch")
	}
	tail := body[switchAt:]
	for _, want := range []string{
		`troubleshoot.addAction("Check for Problems"`,
		`troubleshoot.addAction("Open Logs"`,
		`troubleshoot.addAction(UninstallPresentation.actionTitle`,
		`menu.addSubmenu("Troubleshoot"`,
	} {
		if !strings.Contains(tail, want) {
			t.Fatalf("状态分支之后缺 %s —— 子菜单没装全或没挂进主菜单", want)
		}
	}
	for _, stray := range []string{
		`menu.addAction("Check for Problems"`,
		`menu.addAction("Open Logs"`,
		`menu.addAction(UninstallPresentation.actionTitle`,
		`menu.addAction("Set Up a New Server…"`,
	} {
		if strings.Contains(tail, stray) {
			t.Fatalf("状态分支之后一级菜单里仍有 %s —— 精简的东西又长回来了", stray)
		}
	}
}

// 「Replace Configuration…」只在没有服务器窗口(旧 Guardian)时留在一级菜单,
// 判据是 replaceConfigurationLivesInMenu(纯函数);有窗口时它和「New Server…」
// 是窗口里的按钮,回调接到 main.swift 原来那两个动作上。
func TestMacMenuMovesServerActionsIntoTheServersWindow(t *testing.T) {
	code := menuMainSwiftCode(t)
	body, ok := swiftFunctionBody(code, "private func rebuildMenu()")
	if !ok {
		t.Fatal("读不出 rebuildMenu 的函数体")
	}
	idx := strings.Index(body, "#selector(replaceConfiguration)")
	if idx < 0 {
		t.Fatal("旧 Guardian 那条路上没有 Replace Configuration 入口 —— 换服务器又只能开终端")
	}
	before := body[:idx]
	gate := strings.LastIndex(before, "if replaceConfigurationLivesInMenu(")
	other := strings.LastIndex(before, "if ")
	if gate < 0 || gate != other {
		t.Fatalf("Replace Configuration 不是由 replaceConfigurationLivesInMenu 直接门控的(最近的条件在 %d,判据在 %d)", other, gate)
	}
	for _, want := range []string{
		"controller.onDeploy = ",
		"self?.openDeployWindow()",
		"controller.onReplaceConfiguration = ",
		"self?.replaceConfiguration()",
	} {
		if !strings.Contains(code, want) {
			t.Fatalf("服务器窗口的回调没接上:缺 %s", want)
		}
	}
	window := blankSwiftStringLiterals(stripSwiftComments(menuServersWindowSource(t)))
	for _, want := range []string{"#selector(deployServer)", "#selector(replaceConfiguration)", "onDeploy?()", "onReplaceConfiguration?()"} {
		if !strings.Contains(window, want) {
			t.Fatalf("ServersWindow 里缺 %s —— 按钮没摆或没接回调", want)
		}
	}
}

func menuServersWindowSource(t *testing.T) string {
	t.Helper()
	source, err := os.ReadFile(filepath.Join(
		"..", "..", "apps", "macos", "BxMenu", "Sources", "BxMenu", "ServersWindow.swift"))
	if err != nil {
		t.Fatalf("读不到 ServersWindow.swift:%v —— 守卫已经失效,先修守卫", err)
	}
	return string(source)
}
