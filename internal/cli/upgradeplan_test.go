package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/elevate"
)

// Guardian 没在跑、配置也还不可用(还没跑过 bx setup)就只装文件 ——
// 没有运行中的进程要换,也就没有断网的理由;而硬把一个读不到配置的 Guardian
// bootstrap 起来,配上 KeepAlive=true 就是崩溃循环。
func TestUpgradeStepsWithoutRunningGuardianOnlyInstalls(t *testing.T) {
	got := upgradeSteps(false, false, false)
	want := []UpgradeStep{UpgradeInstallFiles}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("steps = %v, want %v", got, want)
	}
	if len(upgradeSteps(false, true, false)) != 1 {
		t.Fatal("Guardian 没在跑时,desired 是什么都不该多做")
	}
}

// **全新安装必须把 Guardian 服务拉起来**,只要配置已经可用。
//
// 真机实测(2026-08-10):装完之后 plist 写好了但从没被 bootstrap 过,
// guardian.sock 要等到第一次 `sudo bx up` 才存在 —— 而菜单栏的免密开关正是连
// 那个 socket。于是「全新安装 → 点菜单里的 Start Protection」**必然失败**,
// 且三处都不留痕(Guardian 没起来所以没日志、菜单自己不记失败、403 也不记)。
// 升级路径一直没这个问题,因为 restartGuardianForUpgrade 会 bootout 再 bootstrap;
// 也就是说菜单栏第①期的免密开关,在全新安装这条路上从没能工作过。
//
// 它**不开保护**:Guardian 起来时 desired 还是 off,只是开始服务控制 socket。
func TestUpgradeStepsOnFreshInstallStartsGuardianWhenConfigIsUsable(t *testing.T) {
	for _, desired := range []bool{false, true} {
		got := upgradeSteps(false, desired, true)
		want := []UpgradeStep{UpgradeInstallFiles, UpgradeEnableGuardian}
		if len(got) != len(want) {
			t.Fatalf("desired=%v steps = %v, want %v", desired, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("desired=%v steps = %v, want %v", desired, got, want)
			}
		}
		// 全新安装绝不开保护 —— 那是用户的决定,README 也是这么承诺的。
		for _, step := range got {
			if step == UpgradeStartProtection {
				t.Fatalf("desired=%v:全新安装不许开启保护, got %v", desired, got)
			}
		}
	}
}

// 配置还不可用时**绝不**拉 Guardian:daemon 启动要读 /etc/bx/config.yaml 取
// owner_uid,读不出就退出,而 plist 带 KeepAlive=true —— bootstrap 一个起不来的
// Guardian 等于让一台刚装好的机器每秒重启它一次,而安装报告说完成。
func TestUpgradeStepsOnFreshInstallSkipsGuardianWhenConfigIsUnusable(t *testing.T) {
	for _, step := range upgradeSteps(false, true, false) {
		if step == UpgradeEnableGuardian {
			t.Fatal("配置还读不出来时不许 bootstrap Guardian —— KeepAlive 会把它变成崩溃循环")
		}
	}
}

// 关键顺序:停保护必须排在装文件之前。
//
// 它让网络在整个换装期间是直连可用的,于是其后任何一步失败,最差只是「没有保护」,
// 而不是「路由指向已消失的 TUN、整机断网」—— 对一个翻墙工具,后者意味着用户连
// 重装包都下不了。
func TestUpgradeStepsStopProtectionBeforeInstalling(t *testing.T) {
	steps := upgradeSteps(true, true, true)
	stop, install := -1, -1
	for i, s := range steps {
		switch s {
		case UpgradeStopProtection:
			stop = i
		case UpgradeInstallFiles:
			install = i
		}
	}
	if stop < 0 || install < 0 {
		t.Fatalf("steps = %v, 必须同时包含停保护与装文件", steps)
	}
	if stop > install {
		t.Fatalf("停保护(%d)必须早于装文件(%d),否则失败会留下断网状态", stop, install)
	}
}

// 升级前开着,升级后要开回来;原本关着就不要擅自打开。
func TestUpgradeStepsRestoreDesiredState(t *testing.T) {
	on := upgradeSteps(true, true, true)
	if on[len(on)-1] != UpgradeStartProtection {
		t.Fatalf("原本开着,最后一步必须是起保护,实际 %v", on)
	}
	for _, s := range upgradeSteps(true, false, true) {
		if s == UpgradeStartProtection {
			t.Fatal("原本关着,不得擅自打开保护")
		}
	}
}

// Guardian 必须被重启,否则换了符号链接也没用 —— 已在跑的进程不会因此换代码。
// 这正是 2026-08-08 那次真机事故的根因。
func TestUpgradeStepsAlwaysRestartGuardianWhenItIsRunning(t *testing.T) {
	for _, desired := range []bool{true, false} {
		found := false
		for _, s := range upgradeSteps(true, desired, true) {
			if s == UpgradeRestartGuardian {
				found = true
			}
		}
		if !found {
			t.Fatalf("desired=%v:Guardian 在跑就必须重启它", desired)
		}
	}
}

// 确认文案必须明说会断网,而不是含糊的「可能有短暂中断」。
func TestUpgradeConfirmMessageStatesTheOutage(t *testing.T) {
	on := upgradeConfirmMessage(true)
	for _, must := range []string{"network drops", "restart protection"} {
		if !strings.Contains(on, must) {
			t.Fatalf("确认文案必须包含 %q,实际 = %q", must, on)
		}
	}

	// desiredOn=false 时 Guardian 停/重启的是空转(没有 Core、没有 TUN、没有
	// 劫持路由可言),不该断言一个不会发生的断网 —— Guardian plist 是
	// KeepAlive=true,"Guardian 在跑、保护未开启" 正是任何一次 bx down 之后的
	// 常态,不是边角情况。
	off := upgradeConfirmMessage(false)
	if strings.Contains(off, "network drops") {
		t.Fatalf("保护未开启时不该声称会断网,实际 = %q", off)
	}
}

// 失败文案必须说清「现在处于什么状态」,而不只是抛出错误。
func TestUpgradeFailureMessageSaysNetworkIsUsable(t *testing.T) {
	msg := upgradeFailureMessage(UpgradeStartProtection, errors.New("boom"))
	if !strings.Contains(msg, "The network still works") {
		t.Fatalf("装文件之后的失败必须说明网络仍可用(直连),实际 = %q", msg)
	}
	if !strings.Contains(msg, "uninstall") {
		t.Fatalf("失败必须给出真正管用的下一步(卸载重装),实际 = %q", msg)
	}
}

// 停保护失败时不得声称「当前状态未变」。
//
// macOSDownLifecycleDetailed 只会在 forcedMacOSTeardown 报错时返回错误,而那条
// 逃生路径按设计把六个破坏性步骤全做完再报告(记 desired=off、请 Core 退出、
// bootout Guardian、删屏障阻断路由、还原 DNS)。说「状态未变」是把机器的实际
// 状态说反了。
func TestUpgradeFailureMessageForStopDoesNotClaimNothingChanged(t *testing.T) {
	msg := upgradeFailureMessage(UpgradeStopProtection, errors.New("boom"))
	if strings.Contains(msg, "nothing changed") {
		t.Fatalf("强制拆除已经跑过了,不得声称状态未变,实际 = %q", msg)
	}
	if !strings.Contains(msg, "has not been confirmed") {
		t.Fatalf("必须说明网络状态未经确认,实际 = %q", msg)
	}
	if !strings.Contains(msg, "are not installed") {
		t.Fatalf("必须说清文件还没换(升级没开始),实际 = %q", msg)
	}
}

// 走过强制拆除之后,不得替 bx down 说出它自己拒绝说的那句话。
func TestUpgradeFailureMessageWithoutRestoredNetworkDoesNotPromiseConnectivity(t *testing.T) {
	msg := upgradeFailureMessageWithNetwork(UpgradeStartProtection, errors.New("boom"), false)
	if strings.Contains(msg, "The network still works") {
		t.Fatalf("强制拆除是 best-effort,不得断言网络可用,实际 = %q", msg)
	}
	if !strings.Contains(msg, "has not been confirmed") || !strings.Contains(msg, "uninstall") {
		t.Fatalf("必须说明未经确认并给出下一步,实际 = %q", msg)
	}
	// 干净停过保护那条路的措辞不变(既有断言仍然成立)。
	clean := upgradeFailureMessageWithNetwork(UpgradeStartProtection, errors.New("boom"), true)
	if !strings.Contains(clean, "The network still works") {
		t.Fatalf("干净路径仍应如实告知网络可用,实际 = %q", clean)
	}
}

// 那句建议用户照做也没用:bx down 的干净路径不 bootout Guardian,
// 所以 down/up 只会让同一个旧 Guardian 重起一个旧 Core(2026-08-08 真机实证)。
// 照做也没用的指引比没有指引更糟 —— 它让用户以为自己已经处理过了。
func TestAppInstallDoesNotAdviseTheIneffectiveDownUp(t *testing.T) {
	source, err := os.ReadFile("appinstall_darwin.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	if strings.Contains(text, "bx down && "+elevate.Prefix+"bx up") {
		t.Fatal("这句建议无效,不得再出现:app-install 必须自己把升级做完")
	}
	// 编排搬进了 upgraderun.go(为了让顺序可测),所以链条分两段查:
	// app-install 走 runUpgrade,runUpgrade 按 upgradeSteps 执行。
	if !strings.Contains(text, "runUpgrade(") {
		t.Fatal("app-install 必须经 runUpgrade 执行完整升级")
	}
	orchestrator, err := os.ReadFile("upgraderun.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(orchestrator), "upgradeSteps(") {
		t.Fatal("runUpgrade 必须按 upgradeSteps 执行,而不是自己另编一套顺序")
	}
}

// up 不能在自己是旧版时若无其事地报 Protected。
func TestUpVersionMismatchIsReported(t *testing.T) {
	if got := upVersionMismatchMessage("phase2", "phase2"); got != "" {
		t.Fatalf("版本一致时不该提示,实际 = %q", got)
	}
	if got := upVersionMismatchMessage("", "phase2"); got != "" {
		t.Fatalf("信息不全时不该猜,实际 = %q", got)
	}
	msg := upVersionMismatchMessage("dev", "phase2")
	if msg == "" {
		t.Fatal("Guardian 跑着旧版而盘上是新版,必须提示")
	}
	// 给出的命令必须是真正管用的那条。上次事故的根因正是一句看起来权威、
	// 执行起来无效的建议。
	if strings.Contains(msg, "bx down && "+elevate.Prefix+"bx up") {
		t.Fatalf("不得给出那条无效建议,实际 = %q", msg)
	}
	// 2026-09-25 起**不许**再推荐那条切换命令:它的「停保护」不装屏障,切换那几秒里流量
	// 无保护地直连(所有者:断网可以,泄漏 IP 不行)。屏障下的切换落地之前,只如实说处境。
	if strings.Contains(msg, "app-install") || strings.Contains(msg, upgradeSwitchCommand) {
		t.Fatalf("推荐了会泄漏 IP 的切换命令,实际 = %q", msg)
	}
}

// 那条命令必须**照抄下来就能跑通**。
//
// 上一轮把「无效的 down && up」换成了 `sudo bx app-install` —— 同样跑不通:
// /usr/local/bin/bx 是 bridge,exec 到 runtime/<version>/bx,而 appInstallAction
// 用 os.Executable() 反推 --app-source,那条路径不在任何 Bx.app 里,于是必然
// 以 "is not inside a Bx.app bundle; pass --app-source" 退出。换掉一句无效建议
// 却换上另一句无效建议,用户看到的差别只是错误信息不同。
//
// 这里用**生产代码自己的解析器**(bundleRootFromExecutable,就是 app-install
// 定 --app-source 的那一跳)检查命令里的可执行文件,而不是比对一个字符串常量:
// 只有它说得通,这条命令才真的能启动一次升级。
// **2026-09-18 改了判据但没有放松它。** 命令从「跑 bundle 里那份 bx-cli」换成
// 「显式给 --app-source」之后,「反推得出包根」这个断言对它已经不适用 —— 而它
// 要守的性质没变:**app-install 必须知道 --app-source 是什么**。所以判据改成
// 「两条路任一条走得通」:显式给了就验那个值,没给就验反推。把这条测试删掉换一
// 条只比字符串的,才是放松。
func TestUpgradeSwitchCommandCanActuallyRun(t *testing.T) {
	fields := strings.Fields(upgradeSwitchCommand)
	if len(fields) < 3 || fields[0] != "sudo" || fields[2] != "app-install" {
		t.Fatalf("命令形如 `sudo <可执行文件> app-install [...]`,实际 = %q", upgradeSwitchCommand)
	}
	if i := slices.Index(fields, "--app-source"); i >= 0 {
		if i+1 >= len(fields) {
			t.Fatalf("--app-source 后面没有值:%q", upgradeSwitchCommand)
		}
		if fields[i+1] != darwinAppBundlePath {
			t.Fatalf("--app-source = %q,want %q(安装目的地)", fields[i+1], darwinAppBundlePath)
		}
	} else {
		root, err := bundleRootFromExecutable(fields[1])
		if err != nil {
			t.Fatalf("没有显式给 --app-source,那么命令里的可执行文件必须能反推出 Bx.app 包根"+
				"(app-install 正是这么定 --app-source 的):%v", err)
		}
		if root != darwinAppBundlePath {
			t.Fatalf("推出的包根 = %q,want %q(安装目的地)", root, darwinAppBundlePath)
		}
	}
	// `sudo bx app-install` 是 bridge,反推不出包根 —— 不得回到这个形式。
	if _, err := bundleRootFromExecutable("/usr/local/bin/bx"); err == nil {
		t.Fatal("test premise 失效:bridge 路径本应反推不出 Bx.app 包根")
	}
	// 裸的 `sudo bx app-install`(不带 --app-source)经 bridge 跑必然报
	// not inside a Bx.app bundle —— 不得回到那个形式。判据要连「后面跟不跟
	// --app-source」一起看,否则新形式会被它自己误伤。
	if msg := upVersionMismatchMessage("dev", "phase2"); strings.Contains(msg, elevate.Prefix+"bx app-install") &&
		!strings.Contains(msg, "--app-source") {
		t.Fatal("不得建议裸 `" + elevate.Prefix + "bx app-install`:经 bridge 跑必然报 not inside a Bx.app bundle")
	}
}

// bx up 必须真的把版本不一致的提示打印出来,不能只在别处定义一个没人调用的
// 纯函数 —— 那样 upVersionMismatchMessage 本身测得再绿,也拦不住"忘记接线"
// 这一类回归(2026-08-08 事故正是"看得到却没说出来")。macOSUpAction 依赖真实
// Guardian socket 与 /etc/bx/config.yaml,没法直接跑起来断言 stderr,所以像
// TestAppInstallDoesNotAdviseTheIneffectiveDownUp 一样读源码确认调用点存在:
// 删掉那一行调用,这条测试就会变红,而只测纯函数的测试不会。
func TestMacOSUpActionWiresVersionMismatchMessage(t *testing.T) {
	source, err := os.ReadFile("guardian.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	start := strings.Index(text, "\nfunc macOSUpAction(")
	if start < 0 {
		t.Fatal("macOSUpAction 未找到")
	}
	body := text[start+1:]
	if next := strings.Index(body, "\nfunc "); next >= 0 {
		body = body[:next]
	}
	if !strings.Contains(body, "upVersionMismatchMessage(") {
		t.Fatal("macOSUpAction 必须调用 upVersionMismatchMessage 并打印非空结果,否则版本不一致时用户什么都看不到")
	}
	if !strings.Contains(body, "result.Status.GuardianVersion") || !strings.Contains(body, "result.Status.RuntimeVersion") {
		t.Fatal("必须用 macOSUpLifecycle 实际返回的 Guardian 状态,而不是猜一个版本号")
	}
}

// 「问不出来」那句话也必须按 desiredOn 分叉,不能写死「会断网」。
//
// 走确认这条路的条件是「Guardian 在跑」,不是「保护开着」—— 而 Guardian 在跑、
// 保护关着,正是任何一次 bx down 之后的常态。上一版把「会断网的升级」写死在哨兵
// 里,于是同一次运行先打印「当前保护未开启,不会影响网络」,紧接着报「会断网的
// 升级」,两句自相矛盾。这是 f7f976e 修过的同一个错误换了件衣服。
func TestUpgradeCannotAskMessageDoesNotInventAnOutage(t *testing.T) {
	on := upgradeCannotAskMessage(true)
	if !strings.Contains(on, "network drops") {
		t.Fatalf("保护开着时必须说明会断网,实际 = %q", on)
	}
	off := upgradeCannotAskMessage(false)
	if strings.Contains(off, "network drops") {
		t.Fatalf("保护未开启时不该声称会断网,实际 = %q", off)
	}
	for _, msg := range []string{on, off} {
		if !strings.Contains(msg, "--yes") {
			t.Fatalf("必须告诉调用方怎么显式表态,实际 = %q", msg)
		}
	}
}

// 「保护正在运行」这个说法已经被修过两次、regress 了两次(README、install.sh 的
// 前言、README.txt 的正文、README.txt 的 Notes、--help),每次都是改了这一处、
// 漏了那一处。它之所以反复回来,是因为它读起来很自然而事实上是错的:确认与中止
// 的真实触发条件是 install.GuardianLoaded ——launchd 里那个 label 加载着,与保护
// 开没开无关。装过 bx 但 bx down 之后,正是这个说法最容易骗到人的状态。
//
// 靠人盯已经证明不行,所以钉住它。
func TestNoUserFacingCopyClaimsProtectionMustBeRunning(t *testing.T) {
	// 必须先转绝对路径:WalkDir 的第一个回调是根目录本身,而相对根 "../.." 的
	// d.Name() 就是 ".." —— 下面那条「跳过隐藏目录」会当场把整个遍历 SkipDir 掉,
	// 守卫变成永远为绿的空壳(变异测试当场逮到过一次)。
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	// 排除本文件:它必然包含这个字符串(注释、搜索字面量、失败信息各一处),
	// 否则守卫会检出自己。用 runtime.Caller 而不是写死文件名,改名也不会失效。
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("拿不到本测试文件路径")
	}
	// docs/ 是设计与计划的历史记录,不是用户可见文案,不在此列。
	skipDirs := map[string]bool{".git": true, "docs": true, "dist": true, "node_modules": true}
	var offenders []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		// 读不动的路径跳过而不是整个失败:本守卫的职责是找错误文案,
		// 不是断言文件系统可读(仓库里有 root 拥有的测试日志目录)。
		if err != nil {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") && d.Name() != "." {
				return filepath.SkipDir
			}
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		switch filepath.Ext(path) {
		case ".go", ".sh", ".md", ".swift":
		default:
			return nil
		}
		if path == self {
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		for i, line := range strings.Split(string(body), "\n") {
			if strings.Contains(line, "保护正在运行") {
				offenders = append(offenders, fmt.Sprintf("%s:%d", path, i+1))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) > 0 {
		t.Fatalf("确认/中止的触发条件是「已装过 bx(Guardian 已加载)」,不是「保护正在运行」;"+
			"以下位置仍是旧说法:\n  %s", strings.Join(offenders, "\n  "))
	}
}

// 这条修复指引**不许依赖 bundle 里那个文件的执行位**。
//
// 2026-09-18 真机:一次正常升级之后 Contents/Resources/bx-cli 是 0644(两个写者
// 两份清单,见 update.MacOSAppFileMode),而这条命令正是直接执行它 —— 用户照着
// 敲得到 `command not found`。执行位那个 bug 已经修了,但**一条修复指引的全部
// 职责就是在降级状态下还能跑**,而"安装器把权限位写对了"是它最不该依赖的前提。
//
// 出路是显式给 --app-source:它绕开 appInstallAction 的那一跳反推(那一跳正是
// 裸 `bx app-install` 跑不通的原因,见上一条守卫),同时不执行 bundle 里的任何
// 东西 —— bridge 与 runtime 都是好的,这条路的前提本来就是"runtime 是新的、
// 只有 Guardian 旧"。
//
// **unifiedRepairHint 刻意不跟着改**,由下面那条钉住:它用在 runtime/current
// 坏掉的场景,那时 bridge exec 过去的东西根本不存在,bundle 那份是唯一保证在的
// 二进制 —— 同一个写法在两处有相反的理由。
func TestUpgradeSwitchCommandDoesNotDependOnTheBundleExecBit(t *testing.T) {
	if strings.Contains(upgradeSwitchCommand, "/Contents/Resources/bx-cli") {
		t.Fatalf("这条指引仍在直接执行 bundle 里的 bx-cli,而升级可能没给它执行位:%q", upgradeSwitchCommand)
	}
	if !strings.Contains(upgradeSwitchCommand, "--app-source "+darwinAppBundlePath) {
		t.Fatalf("没有显式给 --app-source,经 bridge 跑必然反推失败:%q", upgradeSwitchCommand)
	}
}

// 相反方向:修坏掉的统一安装时**必须**用 bundle 里那份。
// runtime/current 不完整正是那条提示的前提,而 bridge 会 exec 到那里。
func TestUnifiedRepairHintStillRunsTheBundleCopy(t *testing.T) {
	if !strings.Contains(unifiedRepairHint, darwinAppBundlePath+"/Contents/Resources/bx-cli") {
		t.Fatalf("修复提示不再指向 bundle 里那份 —— runtime 坏掉时 bridge exec 不过去,"+
			"那份是唯一保证在的二进制:%q", unifiedRepairHint)
	}
}

// bx status 必须说出版本漂移(2026-09-25 真机:两次升级之后 Guardian 仍是 v0.4.3,而 status
// 一个字不说、菜单还显示「有更新」)。两个版本相同或问不出来时一个字都不说。
func TestStatusSaysWhenGuardianIsBehindTheInstalledVersion(t *testing.T) {
	drifted := clientStatusReport{ProtectionState: "protected", GuardianVersion: "v0.4.3", RuntimeVersion: "v0.4.5"}
	out := renderClientStatus(drifted)
	if !strings.Contains(out, "v0.4.5 is installed") || !strings.Contains(out, "v0.4.3") {
		t.Fatalf("版本漂移没有说出来:\n%s", out)
	}
	if strings.Contains(out, "app-install") {
		t.Fatalf("status 推荐了会泄漏 IP 的切换命令:\n%s", out)
	}
	for _, same := range []clientStatusReport{
		{ProtectionState: "protected", GuardianVersion: "v0.4.5", RuntimeVersion: "v0.4.5"},
		{ProtectionState: "protected", GuardianVersion: "", RuntimeVersion: "v0.4.5"},
	} {
		if strings.Contains(renderClientStatus(same), "is installed, but Guardian") {
			t.Fatalf("版本一致或问不出来时也说了漂移:%+v", same)
		}
	}
}

// 更新检查拿「装着的那一版」去比,不拿 Guardian 自己的版本:Guardian 落后于
// runtime 时(2026-09-25 真机:v0.4.3 对 v0.4.5),拿后者去比会把已经装好的
// 新版永远报成「有更新」。runtime 读不出来时退回进程自己的版本。
func TestUpdateCheckComparesTheInstalledRuntimeNotTheGuardian(t *testing.T) {
	got := updateCheckInstalledVersion("v0.4.3", func() (string, error) { return "v0.4.5", nil })
	if got != "v0.4.5" {
		t.Fatalf("installed version = %q, want the runtime's v0.4.5", got)
	}
	if newerAvailable(got, "v0.4.5") {
		t.Fatal("an installed v0.4.5 must not be reported as an available update to v0.4.5")
	}
	for name, read := range map[string]func() (string, error){
		"unreadable": func() (string, error) { return "", errors.New("no runtime") },
		"empty":      func() (string, error) { return " ", nil },
		"absent":     nil,
	} {
		if got := updateCheckInstalledVersion("v0.4.3", read); got != "v0.4.3" {
			t.Fatalf("%s runtime: installed version = %q, want the process's own v0.4.3", name, got)
		}
	}
}
