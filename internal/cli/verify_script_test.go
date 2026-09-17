package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
		{"windows_test_typecheck", "Windows 那半的 _test.go —— 上面那圈 go build 不编测试文件,\n" +
			"而 windows-tagged 的行为断言只在 CI 的 windows runner 上跑,写坏了本地一路绿灯"},
		{"integration_test_typecheck", "netns 集成台的 _test.go —— 它们既不被 go build 编(不编测试文件)\n" +
			"也不被 go test ./... 编(缺 integration tag),本机一个字都看不见,只有 CI 那条 sudo 腿会红"},
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
	// 2026-09-08 起原因写在开关行下面那行小字(标红),不再是单独的 Status 行。
	if !strings.Contains(warning, `protectionSwitchRow(subtitle: message, subtitleIsBad: true)`) {
		t.Error("`.warning` 的原因(Repair Required / DNS not managed)不见了 —— 图标说不出原因," +
			"开关下面那行是唯一说得出的地方;删掉它用户就只剩一个「有点不对劲」的图标")
	}
	// **不用固定字节窗口取 rebuildMenu。** `scopeAfter` 的 span 是个常数,而这里
	// 要守的那一句在函数体里的位置会随任何一次无关的插入往后挪 —— 在 rebuildMenu
	// 开头加一行完全无辜的代码就能把它挤出 400 字节之外,守卫**假红**并 blame
	// 错地方(2026-08-22 实测撞到)。恒红的守卫会被下一个人删掉,那等于没有守卫。
	// `swiftFunctionBody` 取的是配平后的**整个**函数体,且在抹白副本上数括号,
	// 不受长度与字符串字面量影响。
	rebuild, ok := swiftFunctionBody(source, "private func rebuildMenu()")
	if !ok {
		t.Fatal("读不出 rebuildMenu() 的函数体 —— 这条守卫已经读不懂它要守的东西,请连同它一起改")
	}
	// 2026-09-08 起更新入口只有一处(addUpdateActionIfAvailable,只在有新版时出现),
	// 常驻版本行连同 updateShownInVersionRow 那个「谁先画了谁」的记账一起删了。
	if strings.Count(rebuild, "addUpdateActionIfAvailable(to: menu)") != 1 {
		t.Error("rebuildMenu 里的更新入口不是恰好一处 addUpdateActionIfAvailable(to: menu) —— " +
			"少了没有更新入口,多了同一件事说两遍")
	}
	if strings.Contains(rebuild, "addVersionRow(") {
		t.Error("常驻版本行又回到菜单里了 —— 它只在有新版时才是信息,平时住在 Troubleshoot ▸ 里")
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

// **verify.sh 与 CI 必须钉同一个 gofumpt 版本。**
//
// 2026-09-15 实测的代价:本机装的是 v0.11.0、CI 钉的是 v0.10.0,而两版真的会
// 打架(v0.10.0 要求多行实参表带尾逗号 + 右括号独占一行,v0.11.0 放松了这条)。
// 于是 `bash scripts/verify.sh` 的「gofumpt (no drift)」恒绿、CI 的 lint 恒红 ——
// **而这个仓库的全部「已验证」都只由本机那一份背书**。一道与它要预演的那道
// 守着不同标准的闸门,比没有这道闸门更糟:它训练人相信自己已经过了。
//
// 判据打在**两个 pin 的值相等**上,不写死某个具体版本 —— 升级 gofumpt 时该做的
// 是两处一起改,而不是回来改这条测试里的第三份拷贝。
func TestVerifyScriptPinsTheSameGofumptAsCI(t *testing.T) {
	read := func(path string) string {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读不出 %s:%v —— 这条守卫读不懂现在的代码了,先修它", path, err)
		}
		return string(b)
	}
	ciPin := regexp.MustCompile(`mvdan\.cc/gofumpt@(v[0-9.]+)`).FindStringSubmatch(read("../../.github/workflows/ci.yml"))
	if ciPin == nil {
		t.Fatal("ci.yml 里找不到钉死的 gofumpt 版本 —— 读不出要比的东西时必须响亮失败")
	}
	verify := read("../../scripts/verify.sh")
	localPin := regexp.MustCompile(`GOFUMPT_VERSION="(v[0-9.]+)"`).FindStringSubmatch(verify)
	if localPin == nil {
		t.Fatal("verify.sh 里找不到 GOFUMPT_VERSION —— 它又退回「谁装的哪一版就是哪一版」了")
	}
	if ciPin[1] != localPin[1] {
		t.Errorf("两道闸门用的不是同一个 gofumpt:CI=%s verify.sh=%s —— 本机绿而 CI 红正是这么来的",
			ciPin[1], localPin[1])
	}
	// **而且 verify.sh 真的要用那个 pin 去跑**,不是留一个没人读的变量。
	if !strings.Contains(verify, `gofumpt@$GOFUMPT_VERSION`) {
		t.Error("verify.sh 声明了 GOFUMPT_VERSION 却没拿它去跑 gofumpt —— 一个没人读的 pin 什么也不钉")
	}
}

// **每一个打 macOS 包的 job 都必须先腾磁盘,而且用的必须是同一份。**
//
// 2026-09-15:`macos-fresh-install` 在 `hdiutil: create failed - No space left
// on device` 上红了 —— 与 v0.4.0 那次发布**同一个根因**。当时的修法只加进了
// `release.yml`,而 `ci.yml` 里这个**同样跑 package-macos-release.sh** 的 job
// 没跟上:一份拷贝修好了,另一份原地不动,而两条腿都会在同一件事上失败。
//
// 判据是**结构**不是拼写:凡是有一步跑 `package-macos-release.sh` 的 job,
// 同一个 job 里必须**在它之前**用上那个共用的腾盘动作。抄第三份进来照样红 ——
// 那正是这条守卫要拦的事。
func TestEveryMacOSPackagingJobFreesDiskFirst(t *testing.T) {
	const action = "./.github/actions/free-macos-disk"
	for _, wf := range []string{"ci.yml", "release.yml"} {
		path := filepath.Join("..", "..", ".github", "workflows", wf)
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读不出 %s:%v —— 这条守卫读不懂现在的代码了,先修它", wf, err)
		}
		jobs := splitWorkflowJobs(string(b))
		if len(jobs) == 0 {
			t.Fatalf("%s 里一个 job 都没扫到 —— 安静地扫了零个的守卫,与没有这条守卫完全一样", wf)
		}
		packaging := 0
		for name, body := range jobs {
			pack := strings.Index(body, "package-macos-release.sh")
			if pack < 0 {
				continue
			}
			packaging++
			free := strings.Index(body, action)
			if free < 0 {
				t.Errorf("%s 的 job %q 打了 macOS 包却没腾磁盘 —— v0.4.0 与 2026-09-15 "+
					"两次都栽在 hdiutil 的 No space left on device 上", wf, name)
				continue
			}
			if free > pack {
				t.Errorf("%s 的 job %q 把腾磁盘排在打包之后 —— 那等于没腾", wf, name)
			}
		}
		if packaging == 0 {
			t.Errorf("%s 里没有任何 job 跑 package-macos-release.sh —— 守卫的锚点漂了", wf)
		}
	}
}

// splitWorkflowJobs 把 workflow 切成「job 名 → 那个 job 的整段文本」。
// 按缩进切:`jobs:` 下面缩进 2 空格的那一层是 job 名。
func splitWorkflowJobs(src string) map[string]string {
	lines := strings.Split(src, "\n")
	start := -1
	for i, line := range lines {
		if strings.HasPrefix(line, "jobs:") {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return nil
	}
	jobs := map[string]string{}
	name, from := "", -1
	flush := func(to int) {
		if name != "" && from >= 0 {
			jobs[name] = strings.Join(lines[from:to], "\n")
		}
	}
	for i := start; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		if indent == 0 {
			break // 回到顶层,jobs 段结束
		}
		if indent == 2 && strings.HasSuffix(trimmed, ":") {
			flush(i)
			name, from = strings.TrimSuffix(trimmed, ":"), i
		}
	}
	flush(len(lines))
	return jobs
}

// **Windows 那条腿要跑的包必须现取,不许是手抄的清单。**
//
// 2026-09-15 把它从 `go test ./...` 收窄成「带 *_windows_test.go 的包 +
// 带 purity_test.go 的纯判据包」。收窄的理由是实测:`go test ./...` 在
// Windows 上红了两个多月,168 条失败绝大多数是 darwin/linux 子系统的测试跑在
// 一台 Windows 主机上;而同一天真机扫描抓到的五条**真**缺陷,没有一条会被
// 那 168 个里的任何一个抓到 —— 红着的腿不是严格,是等于不存在。
//
// 收窄之后最容易出的事是**那份清单变成手抄的**:加一个纯判据包而没人把它
// 加进去,它就永远不在 Windows 上跑过,而没有任何东西会红。判据因此与
// verify.sh 第 14 步同源:从 git ls-files 现取 + 一个都找不到时响亮失败。
func TestWindowsCILegDerivesItsPackageList(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatalf("读不出 ci.yml:%v —— 这条守卫读不懂现在的代码了,先修它", err)
	}
	jobs := splitWorkflowJobs(string(b))
	body, ok := jobs["test-windows"]
	if !ok {
		t.Fatal("ci.yml 里没有 test-windows —— Windows 那一侧此刻一个测试都不跑")
	}
	if !strings.Contains(body, "git ls-files") {
		t.Error("那份清单不是现取的 —— 手抄的清单会漏掉下一个纯判据包,而漏掉不会有任何东西红")
	}
	for _, pattern := range []string{"_windows_test.go", "purity_test.go"} {
		if !strings.Contains(body, pattern) {
			t.Errorf("清单里没有 %s —— 少一组就是少一整类 Windows 覆盖", pattern)
		}
	}
	// 一个都找不到时必须响亮失败:一条安静地跑了零个包的 CI 腿,
	// 与没有这条腿在输出上完全一样,而它看起来更让人放心。
	if !strings.Contains(body, "::error::") {
		t.Error("清单为空时没有响亮失败 —— 那会退化成一条安静地什么都不跑的腿")
	}
	// 全量 `go test ./...` 不许回来:它在这个平台上结构性地红,
	// 而恒红的闸门会被下一个人删掉。
	if strings.Contains(body, "go test ./...") {
		t.Error("test-windows 又跑回全量 go test ./... 了 —— 那条路在这个平台上恒红")
	}
}

// **编译并跑 Swift 套件的跑器,全仓只许有一份。**
//
// 2026-08-01 到 2026-09-16 之间有两份:scripts/test-macos-menu.sh(真正那份),
// 以及 apps/macos/BxMenu/run-swift-tests.sh —— 后者由一个 SwiftPM build-tool 插件
// 在 `swift build` 时顺带跑。当初的计划书白纸黑字写着两份编译清单「人工保持一致」,
// 而**这正是这个仓库反复罚过的那种约定**:它漂了,插件那份停在 7 个套件,真正
// 那份长到 32 个;更要命的是插件那份的 guardian-client 套件编 GuardianClient.swift
// 却没带 2026-08-07 才出现的 GuardianStatus.swift。
//
// 后果不是少跑几个套件,是 `swift build` 从那天起一直失败(cannot find
// 'GuardianStatus' in scope),verify.sh 那一步与 CI 的 macos-app job 一起
// **红了五周**。红着的腿不是严格,是等于不存在。
//
// 判据钉的是缺陷本身:**能编 Swift 的脚本有几个**。钉「插件不在 Package.swift 里」
// 挡不住有人换个方式再挂一份,而多出来的那一份无论怎么挂,都要有人去调 swiftc。
func TestOnlyOneSwiftTestRunnerExists(t *testing.T) {
	root := filepath.Join("..", "..")
	canonical := "scripts/test-macos-menu.sh"

	// **清单从 git ls-files 现取**,与 verify.sh 第 14 步、ci.yml 的 test-windows
	// 同一条:只看被跟踪的文件。走文件系统会撞上本地产物(.build/、.bx-test-logs/,
	// 后者实测还会 permission denied),而那些不是仓库的一部分。
	out, err := exec.Command("git", "-C", root, "ls-files", "*.sh").Output()
	if err != nil {
		t.Fatalf("列不出被跟踪的脚本:%v —— 这条守卫读不懂现在的仓库了,先修它", err)
	}
	files := strings.Fields(string(out))
	if len(files) == 0 {
		t.Fatal("一个被跟踪的 .sh 都没列出来 —— 判据已经认不出它要看的东西了")
	}

	var runners []string
	for _, rel := range files {
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if os.IsNotExist(err) {
			// git ls-files 读的是**索引**。一次删除或改名登记之前,索引里有而
			// 工作树里没有是正常状态,而一个不存在的文件当然不是跑器。
			// 别的读错误仍然响亮失败。
			continue
		}
		if err != nil {
			t.Fatalf("读不出 %s:%v", rel, err)
		}
		// **判据是「它编不编 Tests/ 下的文件」,不是「它调不调 swiftc」。**
		// 后者会把打包脚本(scripts/package-macos-menu.sh,只编 Sources/)
		// 一起网进来,而那不是跑器。注释要先剥掉:verify.sh 的注释里同时写着
		// swiftc 和 Tests/,正是在解释这两步的分工。
		body := stripShellComments(string(b))
		if strings.Contains(body, "swiftc ") && strings.Contains(body, "Tests/") {
			runners = append(runners, rel)
		}
	}

	// 一个都没扫到 ⇒ 判据已经认不出跑器了,这时候必须响亮失败:
	// 一条安静地扫了零个文件的守卫,与没有这条守卫在输出上完全一样。
	if len(runners) == 0 {
		t.Fatal("一个调 swiftc 的脚本都没扫到 —— 要么跑器没了(那就把这条守卫一起删掉)," +
			"要么判据认不出它了")
	}
	if len(runners) != 1 || runners[0] != canonical {
		t.Errorf("编 Swift 套件的脚本不止一份(或不是那一份):%v\n"+
			"唯一那份应当是 %s;多出来的一份会漂,而漂的表现是 swift build 静默红掉",
			runners, canonical)
	}
}

// stripShellComments 去掉整行的 shell 注释。判据要看的是脚本**做**了什么,
// 而这个仓库的注释里经常原样写着它要匹配的那些串。
func stripShellComments(body string) string {
	var kept []string
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}
