package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// scripts/verify.sh 是「提交前验过了」这句话的唯一实现。**它漏掉一步,就等于
// 那一步从此不再被验**,而漏掉的表现是安静的:verify 照样打印 ✓。
//
// 这里钉的是**清单**(哪些步骤必须在场),不是语义 —— 步骤本身对不对由它们各自
// 的工具负责,而「有没有这一步」正是文本匹配恰当的场合(同
// menu_plist_generators_darwin_test.go 那条守卫的定位)。
//
// 读不到脚本必须响亮失败:一个因为拿不到文件而自动通过的守卫,与没有守卫是同一回事。
func TestVerifyScriptCoversEveryGate(t *testing.T) {
	path := filepath.Join("..", "..", "scripts", "verify.sh")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读不到 %s:%v —— 这条守卫失去意义,必须响亮失败而不是放过", path, err)
	}
	script := string(raw)

	for _, gate := range []struct{ needle, why string }{
		{"go build ./...", "编译"},
		{"go vet ./...", "vet"},
		{"go test ./... -count=1", "全量单测(必须带 -count=1,否则可能全是缓存)"},
		{"gofumpt", "格式漂移"},
		{"go test -race", "竞态"},
		{"GOOS=", "交叉编译"},
		{"swift build --package-path apps/macos/BxMenu", "Swift 侧编译(Tests/ 不属于任何 target,只有它编 Sources/)"},
		{"scripts/test-macos-menu.sh", "Swift 测试套件(swift build 编不到 Tests/,两步不可互相替代)"},
		{"macOS menu tests passed", "收尾横幅 —— 脚本提前 exit 0 时退出码是 0,只有它抓得住"},
	} {
		if !strings.Contains(script, gate.needle) {
			t.Errorf("verify.sh 缺少 %q(%s)—— 少一步就等于那一步从此不再被验,而它照样打印 ✓",
				gate.needle, gate.why)
		}
	}

	// **最后一行必须是判据。** 脚本刻意不用 `set -e`(要一次跑完知道全部坏了什么),
	// 代价是每一步都自己收退出码 —— 而漏掉最后那个 exit 的话,它会在有步骤失败时
	// 依然退 0,正好变成它要消灭的那种东西。
	if !strings.Contains(script, `if [ "$failed" -ne 0 ]`) {
		t.Error("verify.sh 没有在结尾按累计失败数退出 —— 它自己会变成一个不会失败的检查")
	}
	// 跳过必须与通过长得不一样,否则「在 Linux 上跑过 verify」会被读成
	// 「macOS 那半也验过了」。
	if !strings.Contains(script, "SKIPPED") {
		t.Error("verify.sh 必须把跳过的步骤显式标出来,不能与通过混为一谈")
	}
}

// 菜单的这两行是**恒定文案**,删掉之后不许悄悄回来。
//
// `Status: Protected` 是三重重复(图标形状 + 标题栏 "Connected" + 这一行);
// `Network changes: …` 是一个永远不变的常量串 —— 安慰文案不是状态,与之前删掉的
// 三行占位符同一类。**一行永远说同一句话的东西不是信息。**
//
// main.swift 编不进 Swift 测试套件,所以这里读源码文本。钉的是「这两句不该出现」,
// 而不是某段逻辑 —— 判定式那种东西文本匹配守不住,而一句写死的文案守得住。
func TestMacMenuDroppedTheConstantRows(t *testing.T) {
	path := filepath.Join("..", "..", "apps", "macos", "BxMenu", "Sources", "BxMenu", "main.swift")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读不到 %s:%v —— 这条守卫失去意义,必须响亮失败", path, err)
	}
	source := string(raw)
	for _, gone := range []struct{ needle, why string }{
		{`menu.addInfo("Status", "Protected")`, "图标与标题栏已经说过两遍了"},
		{`"Network changes"`, "一个永远不变的常量串,是安慰文案不是状态"},
	} {
		if strings.Contains(source, gone.needle) {
			t.Errorf("%s 又回到菜单里了 —— %s", gone.needle, gone.why)
		}
	}
	// **下面两条必须限定作用域,全文搜是假绿。** 变异实测:两个字面量在文件里
	// 各自出现不止一次(Status 行在 warning / updateNeeded 等多个分支都有;
	// `updateShownInVersionRow = false` 还同时是那个属性的**声明**),于是删掉真正
	// 要守的那一处之后,守卫被另一处满足、照样通过。
	warning := scopeAfter(t, source, "case .warning(let message, let version):", 600)
	if !strings.Contains(warning, `menu.addInfo("Status", message)`) {
		t.Error("`.warning` 的 Status 行不见了 —— 那一行装的是原因(Repair Required / " +
			"DNS not managed),图标说不出来;删掉它用户就只剩一个「有点不对劲」的图标")
	}
	rebuild := scopeAfter(t, source, "private func rebuildMenu() {", 400)
	if !strings.Contains(rebuild, "updateShownInVersionRow = false") {
		t.Error("updateShownInVersionRow 没有在 rebuildMenu 开头复位 —— 漏掉它,某一轮" +
			"出现过版本行之后,此后所有轮次的页脚都不再显示更新入口,而且完全静默")
	}
}

// scopeAfter 取 marker 之后的一段源码,让守卫只在**该看的那一处**里找。
//
// 全文 strings.Contains 是这个仓库反复栽过的形状:同一个字面量在别处也出现时,
// 删掉真正要守的那一处,守卫会被别处满足。marker 找不到必须响亮失败 ——
// 一个因为读不懂代码而自动通过的守卫,与没有守卫是同一回事。
func scopeAfter(t *testing.T, source, marker string, span int) string {
	t.Helper()
	i := strings.Index(source, marker)
	if i < 0 {
		t.Fatalf("源码里找不到 %q —— 这条守卫已经读不懂它要守的东西,请连同它一起改", marker)
	}
	end := i + span
	if end > len(source) {
		end = len(source)
	}
	return source[i:end]
}

// **首次引导必须真的接在启动路径上,而且要排在刷新之后。**
//
// 此前双击 Bx.app 之后什么都不发生:菜单栏冒出一个小图标,而没有任何东西告诉用户
// 下一步该点哪里 —— 一个从 dmg 里拖进来的普通用户到这里就卡住了。
//
// main.swift 编不进 Swift 测试套件,所以这条接线只能在源码层钉。判定本身
// (问不问、问哪个)住在 FirstRun.swift,由 FirstRunTests 覆盖。
func TestMacMenuRunsFirstRunGuidanceAfterTheFirstRefresh(t *testing.T) {
	path := filepath.Join("..", "..", "apps", "macos", "BxMenu", "Sources", "BxMenu", "main.swift")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读不到 %s:%v —— 守卫失去意义,必须响亮失败", path, err)
	}
	source := string(raw)

	launch := scopeAfter(t, source, "func applicationDidFinishLaunching", 900)
	if !strings.Contains(launch, "runFirstRunGuidance()") {
		t.Error("启动路径没有调用 runFirstRunGuidance —— 双击之后又会变回「什么都不发生」")
	}
	// **必须在刷新的完成回调里,不是紧跟在它后面。**
	//
	// 这条守卫第一版钉的是「排在 refresh 之后」,而那个判据本身是错的:refresh 把
	// 采集扔到后台队列,结果稍后才在主线程写回 state。紧跟其后读到的是初始值,
	// firstRunAction 永远落到 default,**引导从来不触发** —— 而那正是这个功能的
	// 全部意义。审查抓到的,不是测试抓到的。
	if !strings.Contains(launch, "refresh(userInitiated: false) {") {
		t.Error("引导没有挂在 refresh 的完成回调上 —— refresh 是异步的," +
			"紧跟其后读到的是上一轮(启动时就是初始值)的 state,引导会永远不触发")
	}
	// 判定不许搬回 main.swift:那里编不进测试套件。
	if strings.Contains(source, "func firstRunAction(") {
		t.Error("firstRunAction 被搬进了 main.swift —— 那里编不进 Swift 测试套件")
	}
	// 装完那一步同理:读到装之前的状态就会刚装完又弹一次「Install bx?」。
	// **按函数切,不按字节窗口切。** 第一版用了 1400 字节的窗口,而目标恰好落在
	// 窗口外一点点 —— 固定窗口的脆弱正是本仓库记过的那条教训。
	installer, ok := swiftFunctionBody(swiftCodeOnly(source), "private func runEmbeddedInstaller(")
	if !ok {
		t.Fatal("找不到 runEmbeddedInstaller —— 守卫读不懂现在的代码了,请连同它一起改")
	}
	if !strings.Contains(installer, "refresh(userInitiated: true) {") {
		t.Error("装完之后的引导没有挂在刷新的完成回调上 —— 会读到装之前的状态,重复弹安装框")
	}
}
