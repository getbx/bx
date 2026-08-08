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
	// 按**函数**切,不按字节窗口切。固定字节窗口会从 installBx 一路读进
	// repairBx:删掉 Install 那句告知,Repair 的同一句会滑进窗口,测试照样绿
	// (复审实测,当时只剩 27 字节余量),于是 GUI 用户点 Install bx… 断网前
	// 一个字的警告都看不到 —— 而 --yes 又跳过了本可兜住的 CLI 提示。
	for _, fn := range []string{"installBx", "repairBx"} {
		body := swiftFuncBody(t, text, fn)
		if !strings.Contains(body, "network drops") {
			t.Fatalf("%s 的确认文案必须说明会断网(GUI 用户看不到 CLI 的提示)", fn)
		}
	}
}

// swiftFuncBody 截出一个 Swift 方法体:从 `func <name>(` 到下一个 `func ` 为止。
func swiftFuncBody(t *testing.T, text, name string) string {
	t.Helper()
	marker := "func " + name + "("
	start := strings.Index(text, marker)
	if start < 0 {
		t.Fatalf("找不到 Swift 方法 %s", name)
	}
	rest := text[start+len(marker):]
	if next := strings.Index(rest, "func "); next >= 0 {
		rest = rest[:next]
	}
	return rest
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
	agreed, err := confirmPrompt(strings.NewReader("y\n"), false, &out, "升级需要重启保护,期间会断网几秒。现在继续吗?")
	if agreed {
		t.Fatal("没有终端时必须取消,哪怕 stdin 里躺着一个 y")
	}
	// 而且必须**报错**而不是静静地返回「用户说不」:后者会让调用它的脚本
	// (install.sh 在 set -e 下)接着打印「完成」,把没做的升级报成成功。
	if err == nil {
		t.Fatal("问不出来必须回传错误,不能与「用户明确说不」同形")
	}
	if !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("必须告诉调用方怎么显式表态,实际 = %q", err)
	}
}

// 有终端时才真的问,并且照答案办。
func TestConfirmPromptAsksOnATerminal(t *testing.T) {
	var yes, no strings.Builder
	agreed, err := confirmPrompt(strings.NewReader("y\n"), true, &yes, "继续吗?")
	if !agreed || err != nil {
		t.Fatalf("答 y 应继续,实际 agreed=%v err=%v", agreed, err)
	}
	if !strings.Contains(yes.String(), "继续吗?") {
		t.Fatalf("必须把问题打给用户看,实际 = %q", yes.String())
	}
	// 有终端时的「不」是正常结束,不是错误。
	agreed, err = confirmPrompt(strings.NewReader("\n"), true, &no, "继续吗?")
	if agreed || err != nil {
		t.Fatalf("直接回车应取消且不报错,实际 agreed=%v err=%v", agreed, err)
	}
}
