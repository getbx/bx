package cli

import (
	"os"
	"strings"
	"testing"
)

// 菜单是 GUI 用户唯一的入口,而它调 app-install 的方式有两条硬约束,两条都不会
// 产生编译错误:
//
//  1. 必须带 --yes。命令跑在 osascript 里,没有终端;不带它,CLI 会(正确地)
//     因为无法确认而取消,Repair 就成了一个弹完框却什么都没做的按钮 ——
//     正是 2026-08-08 那次事故的形状(菜单报 Repair Required,Repair 修不了)。
//  2. 确认框必须自己说出会断网。CLI 那句「期间会断网几秒」进了一个没人看的
//     管道,这个 NSAlert 是 GUI 用户唯一看得到的告知。
func TestMenuInstallerPassesYesAndWarnsAboutTheOutage(t *testing.T) {
	source, err := os.ReadFile("../../apps/macos/BxMenu/Sources/BxMenu/main.swift")
	if err != nil {
		t.Fatalf("读取菜单源码失败(守卫无法生效): %v", err)
	}
	text := string(source)
	if !strings.Contains(text, "app-install --yes --app-source") {
		t.Fatal("菜单必须以 --yes 调用 app-install,否则 Install/Repair 会被无终端确认挡下")
	}
	for _, action := range []string{"Install bx?", "Repair bx?"} {
		idx := strings.Index(text, action)
		if idx < 0 {
			t.Fatalf("找不到 %q 的确认框", action)
		}
		window := text[idx:min(idx+900, len(text))]
		if !strings.Contains(window, "network drops") {
			t.Fatalf("%q 的确认文案必须说明会断网(GUI 用户看不到 CLI 的提示)", action)
		}
	}
}

func TestBundleRootFromExecutable(t *testing.T) {
	got, err := bundleRootFromExecutable("/Applications/Bx.app/Contents/Resources/bx-cli")
	if err != nil || got != "/Applications/Bx.app" {
		t.Fatalf("got %q err=%v", got, err)
	}
	if _, err := bundleRootFromExecutable("/usr/local/bin/bx"); err == nil {
		t.Fatal("want error for non-bundle executable")
	}
	got, err = bundleRootFromExecutable("/Users/a/Downloads/Bx.app/Contents/Resources/bx-cli")
	if err != nil || got != "/Users/a/Downloads/Bx.app" {
		t.Fatalf("got %q err=%v", got, err)
	}
}

// 升级确认默认是「不」:直接回车、空白、任何看不懂的回答都不算同意 ——
// 这一步会断网,只有明确说 y 才继续。
func TestConfirmationAcceptedOnlyOnExplicitYes(t *testing.T) {
	for _, answer := range []string{"y\n", "Y\n", "yes\n", " yes \n"} {
		if !confirmationAccepted(answer) {
			t.Fatalf("%q 应视为同意", answer)
		}
	}
	for _, answer := range []string{"\n", "", "n\n", "no\n", "ye\n", "sure\n"} {
		if confirmationAccepted(answer) {
			t.Fatalf("%q 不该视为同意", answer)
		}
	}
}

// 没有终端 = 取消。
//
// 默认「继续」会把一次会断网的升级强加给每一个非终端调用方(脚本、CI、打包步骤、
// 将来某个 shell 出去的 bx update),而且无从退出 —— 一个取消不掉的 --yes 不是确认。
// 需要在非交互环境里升级的调用方显式加 --yes(菜单就是这么做的)。
func TestConfirmPromptCancelsWithoutATerminal(t *testing.T) {
	var out strings.Builder
	if confirmPrompt(strings.NewReader("y\n"), false, &out, "升级需要重启保护,期间会断网几秒。现在继续吗?") {
		t.Fatal("没有终端时必须取消,哪怕 stdin 里躺着一个 y")
	}
	if !strings.Contains(out.String(), "--yes") {
		t.Fatalf("取消时必须告诉调用方怎么显式表态,实际 = %q", out.String())
	}
}

// 有终端时才真的问,并且照答案办。
func TestConfirmPromptAsksOnATerminal(t *testing.T) {
	var yes, no strings.Builder
	if !confirmPrompt(strings.NewReader("y\n"), true, &yes, "继续吗?") {
		t.Fatal("答 y 应继续")
	}
	if !strings.Contains(yes.String(), "继续吗?") {
		t.Fatalf("必须把问题打给用户看,实际 = %q", yes.String())
	}
	if confirmPrompt(strings.NewReader("\n"), true, &no, "继续吗?") {
		t.Fatal("直接回车应取消")
	}
}
