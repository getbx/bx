//go:build darwin

package cli

import (
	"strings"
	"testing"
)

// —— 升级之后菜单自己换成新版(known-gaps A13,2026-09-26)——
//
// 判据(什么时候退出)住在 `menuShouldRelaunchForNewBundle`,由 Swift 套件钉住;
// 这里钉的是**接线**:那个判据在 `main()` 里真的被调到、喂进去的是真的值 ——
// 一个写好、测好、却零调用方的判据与没有这个修复在真机上完全一样(第六种失效写法)。
func TestMacMenuRelaunchesItselfWhenTheBundleIsReplaced(t *testing.T) {
	src := stripSwiftComments(menuMainSwiftSource(t))

	// ① 每一轮刷新落定之后都判一次。
	apply, ok := swiftFunctionBody(src, "private func applyRefresh(_ outcome: RefreshOutcome, capturedGeneration: Int) {")
	if !ok {
		t.Fatal("main.swift 里找不到 applyRefresh —— 锚点漂了,回来重判")
	}
	if !strings.Contains(apply, "relaunchIfBundleReplaced()") {
		t.Error("applyRefresh 不再调 relaunchIfBundleReplaced —— 升级之后菜单会一直停在旧版本")
	}

	// ② 启动时记下的版本必须来自盘上那一份(同一个读法),否则两边永远不可比。
	launch, ok := swiftFunctionBody(src, "func applicationDidFinishLaunching(_ notification: Notification) {")
	if !ok {
		t.Fatal("main.swift 里找不到 applicationDidFinishLaunching —— 锚点漂了")
	}
	if !strings.Contains(launch, "launchedBundleVersion = bundleReleaseVersion()") {
		t.Error("启动时没有记下 bundleReleaseVersion() —— 没有基线,判据永远读到 nil、永远不重启")
	}

	// ③ 判据被调到,喂的是真值,退出码是那个非零常量。
	body, ok := swiftFunctionBody(src, "private func relaunchIfBundleReplaced() {")
	if !ok {
		t.Fatal("main.swift 里找不到 relaunchIfBundleReplaced")
	}
	for _, want := range []string{
		"menuShouldRelaunchForNewBundle(",
		"launchedVersion: launchedBundleVersion",
		"onDiskVersion: onDisk",
		"let onDisk = bundleReleaseVersion()",
		`environment["XPC_SERVICE_NAME"] == menuLaunchdLabel`,
		"exit(menuRelaunchExitCode)",
		"NSApp.modalWindow != nil", "menuIsOpen", "updateInFlight != nil", "toggleInFlight != nil",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("relaunchIfBundleReplaced 缺 %s —— 判据喂的不是真值,或退出之后没人拉起", want)
		}
	}
	// busy 不许是字面量:写死 false 就会在用户填表单时把窗口一起抹掉。
	if strings.Contains(body, "busy: false") || strings.Contains(body, "launchdManaged: true") {
		t.Error("relaunchIfBundleReplaced 把 busy / launchdManaged 写成了字面量")
	}

	// ④ Swift 那份 label 与安装器写进 plist 的是同一个 —— 不一致时 XPC_SERVICE_NAME
	//    永远对不上,修复在真机上静默失效。
	model := readMenuSwiftSource(t, "InstallPresentation.swift")
	if !strings.Contains(model, `let menuLaunchdLabel = "`+menuLaunchdLabel+`"`) {
		t.Errorf("Swift 的 menuLaunchdLabel 与 Go 的 %q 不一致", menuLaunchdLabel)
	}
}
