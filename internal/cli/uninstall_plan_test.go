package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/guardian"
)

func TestBuildDarwinUninstallPlanUnified(t *testing.T) {
	plan := buildDarwinUninstallPlan(501, "/Users/alice", true)
	wantCommands := [][]string{
		{"launchctl", "bootout", "system/com.getbx.bx.guard"},
		{"launchctl", "bootout", "gui/501/com.getbx.bx.menu"},
	}
	if !reflect.DeepEqual(plan.LaunchctlCommands, wantCommands) {
		t.Fatalf("commands = %v", plan.LaunchctlCommands)
	}
	for _, want := range []string{
		"/Library/LaunchDaemons/com.getbx.bx.guard.plist",
		"/usr/local/bin/bx",
		"/Library/Application Support/bx/runtime",
		"/Applications/Bx.app",
		"/Users/alice/Library/LaunchAgents/com.getbx.bx.menu.plist",
		"/Users/alice/Library/LaunchAgents/com.ggshr9.bx.menu.plist",
	} {
		if !slices.Contains(plan.RemovePaths, want) {
			t.Fatalf("RemovePaths missing %s: %v", want, plan.RemovePaths)
		}
	}
	for _, keep := range []string{"/etc/bx", "/var/lib/bx"} {
		if slices.Contains(plan.RemovePaths, keep) {
			t.Fatalf("must keep %s", keep)
		}
		if !slices.Contains(plan.KeepPaths, keep) {
			t.Fatalf("KeepPaths missing %s", keep)
		}
	}
}

func TestBuildDarwinUninstallPlanLegacySkipsRuntime(t *testing.T) {
	plan := buildDarwinUninstallPlan(501, "/Users/alice", false)
	if slices.Contains(plan.RemovePaths, "/Library/Application Support/bx/runtime") {
		t.Fatal("legacy layout must not remove runtime root")
	}
	if slices.Contains(plan.RemovePaths, "/Applications/Bx.app") {
		t.Fatal("legacy layout must not remove /Applications/Bx.app")
	}
	for _, want := range []string{
		"/Library/LaunchDaemons/com.getbx.bx.guard.plist",
		"/usr/local/bin/bx",
		"/Users/alice/Library/LaunchAgents/com.getbx.bx.menu.plist",
		"/Users/alice/Library/LaunchAgents/com.ggshr9.bx.menu.plist",
	} {
		if !slices.Contains(plan.RemovePaths, want) {
			t.Fatalf("RemovePaths missing %s: %v", want, plan.RemovePaths)
		}
	}
}

func TestBuildDarwinUninstallPlanNoConsoleUserSkipsUserScope(t *testing.T) {
	plan := buildDarwinUninstallPlan(0, "", true)
	if len(plan.LaunchctlCommands) != 1 {
		t.Fatalf("expected only guard bootout, got %v", plan.LaunchctlCommands)
	}
	if plan.LaunchctlCommands[0][2] != "system/com.getbx.bx.guard" {
		t.Fatalf("unexpected command: %v", plan.LaunchctlCommands[0])
	}
	for _, path := range plan.RemovePaths {
		if slices.Contains([]string{
			"Library/LaunchAgents/com.getbx.bx.menu.plist",
			"Library/LaunchAgents/com.ggshr9.bx.menu.plist",
		}, path) {
			t.Fatalf("must not remove user-scoped path without console user: %v", plan.RemovePaths)
		}
	}
	// runtime/app 仍应清理:统一布局与「是否有控制台用户」无关。
	for _, want := range []string{"/Library/Application Support/bx/runtime", "/Applications/Bx.app"} {
		if !slices.Contains(plan.RemovePaths, want) {
			t.Fatalf("RemovePaths missing %s: %v", want, plan.RemovePaths)
		}
	}
}

// TestUnifiedTeardownNeededExistenceNotHealth 证明判定用的是「存在」而非「健康」:
// 一次半途失败的 app-install(runtime root 目录已建、但没有 current 符号链接,
// 相当于 runtimedir.Installed 会判 false 的损坏态)仍要触发统一卸载分支,否则
// runtime root 与 Bx.app 会被误判成 legacy 布局侥幸存活成 root-owned 孤儿。
func TestUnifiedTeardownNeededExistenceNotHealth(t *testing.T) {
	dir := t.TempDir()
	runtimeRoot := filepath.Join(dir, "runtime") // 目录存在,但没有 "current" 链接:health 检查会判 false
	appBundle := filepath.Join(dir, "Bx.app")    // 不存在

	if err := os.MkdirAll(runtimeRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	if !unifiedTeardownNeeded(runtimeRoot, appBundle) {
		t.Fatal("want true: damaged-but-present runtime root must still trigger unified teardown")
	}
}

// uninstall 保留 /var/lib/bx 是为了保住用户配置,但运行时状态不该跟着活下来——
// 陈旧的 core-process.json 会让重装后的 bx up 失败(真机事故)。
func TestUninstallPlanRemovesRuntimeStateButKeepsConfig(t *testing.T) {
	plan := buildDarwinUninstallPlan(501, "/Users/alice", true)

	joined := strings.Join(plan.RemovePaths, " ")
	for _, runtime := range []string{"core-process.json", "guardian-state.json"} {
		if !strings.Contains(joined, runtime) {
			t.Errorf("运行时状态必须清除:%s;RemovePaths = %v", runtime, plan.RemovePaths)
		}
	}
	keep := strings.Join(plan.KeepPaths, " ")
	if !strings.Contains(keep, "/etc/bx") {
		t.Errorf("用户配置必须保留,KeepPaths = %v", plan.KeepPaths)
	}
	for _, p := range plan.RemovePaths {
		if p == "/etc/bx" || p == "/var/lib/bx" {
			t.Errorf("不得整目录删除配置数据:%s", p)
		}
	}
}

// 挂起文件必须随卸载消失,否则它活过卸载重装 —— darwinCoreProcessStatePath 与
// darwinUpgradeIntentPath 就是为这条教训加进去的。
//
// 断言用的是**字面路径**而不是 darwinMaintenanceHoldPath:用常量做 Contains 只能
// 证明「计划里放了那个常量」,常量本身指错文件照样绿。这条字面量与
// guardian.TestDefaultStoreMaintenanceHoldPath 里的那条对齐,任一侧改动都会转红。
func TestUninstallPlanRemovesMaintenanceHoldButKeepsDataDir(t *testing.T) {
	plan := buildDarwinUninstallPlan(501, "/Users/tester", true)
	if !slices.Contains(plan.RemovePaths, "/var/lib/bx/maintenance-hold.json") {
		t.Fatalf("RemovePaths 缺挂起文件: %v", plan.RemovePaths)
	}
	if darwinMaintenanceHoldPath != "/var/lib/bx/maintenance-hold.json" {
		t.Fatalf("darwinMaintenanceHoldPath = %q,与 guardian 默认路径不一致", darwinMaintenanceHoldPath)
	}
	if slices.Contains(plan.RemovePaths, darwinDataDirPath) {
		t.Fatalf("绝不整目录删 /var/lib/bx: %v", plan.RemovePaths)
	}
}

// **已退休**的升级欠条也必须随卸载消失,理由换了一个但更硬:没有任何东西再写
// 它,可 MigrateLegacyUpgradeIntent 仍会读它 —— 一张活过卸载重装的陈旧欠条会被
// 新装的 Guardian 翻成「desired=on + 一次已武装的挂起」,在一台用户刚刚卸载干净
// 的机器上把保护开回来。
//
// 这条断言此前**根本不存在**:计划以为既有的卸载测试守着它,而
// TestUninstallPlanRemovesRuntimeStateButKeepsConfig 只查 core-process.json 与
// guardian-state.json 两个名字 —— 实测把那一行从 RemovePaths 删掉,整个
// internal/cli 全绿。
func TestUninstallPlanRemovesLegacyUpgradeIntentFile(t *testing.T) {
	plan := buildDarwinUninstallPlan(501, "/Users/tester", true)
	// 字面路径,不用常量:用常量做 Contains 只能证明「计划里放了那个常量」,
	// 常量本身指错文件照样绿(同挂起那条)。这条与 guardian 的
	// OpenDefaultStore().UpgradeIntent 对齐。
	if !slices.Contains(plan.RemovePaths, "/var/lib/bx/upgrade-intent.json") {
		t.Fatalf("RemovePaths 缺 legacy 升级欠条: %v", plan.RemovePaths)
	}
	if darwinUpgradeIntentPath != "/var/lib/bx/upgrade-intent.json" {
		t.Fatalf("darwinUpgradeIntentPath = %q,与 guardian 默认路径不一致", darwinUpgradeIntentPath)
	}
}

// 菜单栏 agent 带 KeepAlive 之后,一次失败的 gui 域 bootout 不再是良性的:job
// 留在 launchd 里,而卸载紧接着删掉 /Applications/Bx.app,launchd 就会每 ~10s
// 重拉一个已不存在的二进制刷屏 menu.err.log,直到用户注销——用户却以为卸干净了。
// 故 gui 域 bootout 失败必须先用 asuser 重试一次(同 menuBootstrapCommand 的修复)。
func TestGUIBootoutRetriesViaAsuserSoKeepAliveJobCannotSurviveUninstall(t *testing.T) {
	got := asuserBootoutFallback([]string{"launchctl", "bootout", "gui/501/com.getbx.bx.menu"})
	want := []string{"launchctl", "asuser", "501", "launchctl", "bootout", "gui/501/com.getbx.bx.menu"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("gui bootout fallback = %v, want %v", got, want)
	}
}

func TestAsuserBootoutFallbackOnlyForGUIBootout(t *testing.T) {
	for name, args := range map[string][]string{
		"system domain has no gui session to enter": {"launchctl", "bootout", "system/com.getbx.bx.guard"},
		"not a bootout":                   {"launchctl", "kickstart", "gui/501/com.getbx.bx.menu"},
		"gui domain without uid":          {"launchctl", "bootout", "gui/"},
		"gui domain with non-numeric uid": {"launchctl", "bootout", "gui/alice/com.getbx.bx.menu"},
		"already an asuser retry":         {"launchctl", "asuser", "501", "launchctl", "bootout", "gui/501/com.getbx.bx.menu"},
	} {
		if got := asuserBootoutFallback(args); got != nil {
			t.Errorf("%s: want no fallback, got %v", name, got)
		}
	}
}

func TestUnifiedTeardownNeededFalseWhenNeitherExists(t *testing.T) {
	dir := t.TempDir()
	if unifiedTeardownNeeded(filepath.Join(dir, "runtime"), filepath.Join(dir, "Bx.app")) {
		t.Fatal("want false: neither artifact present means legacy layout")
	}
}

// launchctl 用 ESRCH(退出码 3)表达「没有这个服务」。而卸载要的**正是**这个
// 终态——服务不在了。把它当成错误刷一行红字只会吓用户:真机 2026-08-06,
// 强制拆除已经 bootout 过 Guardian,随后的 sudo bx uninstall 再 bootout 自然
// exit 3,用户于是看到
//
//	! launchctl bootout system/com.getbx.bx.guard: exit status 3
//
// 排在一堆 ✓ 之上,像是卸载出了问题,实际上完全正常。
func TestBootoutAlreadyGoneIsNotAFailure(t *testing.T) {
	const esrch, other = 3, 1

	if !launchctlBootoutAlreadyGone([]string{"launchctl", "bootout", "system/com.getbx.bx.guard"}, esrch) {
		t.Error("bootout 得到 ESRCH(3)= 服务本来就不在,是卸载想要的终态,不该报错")
	}
	// asuser 重试形态也要认得
	if !launchctlBootoutAlreadyGone([]string{"launchctl", "asuser", "501", "launchctl", "bootout", "gui/501/com.getbx.bx.menu"}, esrch) {
		t.Error("asuser 形态的 bootout 同样要认 ESRCH")
	}
	// 其它退出码是真失败,必须继续报
	if launchctlBootoutAlreadyGone([]string{"launchctl", "bootout", "system/com.getbx.bx.guard"}, other) {
		t.Error("非 ESRCH 的失败仍是失败,不得被吞掉")
	}
	// 非 bootout 命令不适用这条豁免
	if launchctlBootoutAlreadyGone([]string{"launchctl", "bootstrap", "gui/501", "/tmp/x.plist"}, esrch) {
		t.Error("只有 bootout 才有「本来就没加载 = 成功」的语义")
	}
	// 取不到退出码(命令根本没跑起来)不算「本来就没加载」
	if launchctlBootoutAlreadyGone([]string{"launchctl", "bootout", "system/com.getbx.bx.guard"}, -1) {
		t.Error("拿不到退出码时不得假定服务不存在")
	}
}

// 新增的 Guardian 状态文件必须进卸载清单。
//
// **判据是「guardian 声明的默认路径」,不是一个抄过来的字面量** —— 两处各写
// 一份路径,改一处忘改另一处不会有编译错误,而后果是卸载之后重装看到一份
// 指向已经不存在的服务器名的历史。
func TestUninstallRemovesTheThroughputHistory(t *testing.T) {
	plan := buildDarwinUninstallPlan(501, "/Users/someone", true)
	for _, path := range plan.RemovePaths {
		if path == guardian.DefaultThroughputHistoryPath {
			return
		}
	}
	t.Fatalf("卸载计划里没有 %s:%v", guardian.DefaultThroughputHistoryPath, plan.RemovePaths)
}

// —— 卸载必须等 launchd 真的把 job 拆掉,再去删它的文件 ——
//
// **2026-09-15 CI 抓到的,而这一步此前从没被执行到过**:`macos-fresh-install`
// 那条腿在它前面那条断言上红了两个月,于是卸载这一步一次都没跑过。跑通之后
// 当场现形:`launchctl bootout` 返回、紧接着删掉 plist,而 120 毫秒后
// `launchctl print system/com.getbx.bx.guard` **仍然找得到那个 job**。
// bootout 自己没失败(失败会打一行 `! …`,日志里没有)—— 是 2026-08-13 那次
// 「bootout 返回时服务还没真的从域里消失」的同一个竞态。
//
// 后果不是难看:guard 与菜单 agent 都带 KeepAlive,job 留在 launchd 里而文件
// 已经被删,launchd 便会不停重拉一个不存在的二进制,**而用户以为卸干净了**。
// 这正是 runLaunchctlBestEffort 头上那段注释已经描述过的失败形状,只是当时
// 只防住了「bootout 失败」那一半,没防住「bootout 成功但还没生效」这一半。

func TestUninstallWaitsForEveryBootoutTarget(t *testing.T) {
	plan := buildDarwinUninstallPlan(501, "/Users/alice", true)
	targets := bootoutWaitTargets(plan)
	want := map[string]bool{
		"system/" + darwinGuardLaunchdLabel: true,
		"gui/501/" + darwinMenuLaunchdLabel: true,
	}
	if len(targets) != len(want) {
		t.Fatalf("要等的目标数 = %d,应当是 %d:%v", len(targets), len(want), targets)
	}
	for _, target := range targets {
		if !want[target] {
			t.Errorf("多等了一个目标 %q", target)
		}
		delete(want, target)
	}
	for missing := range want {
		t.Errorf("漏掉了 %q —— 它的文件会在 job 还在时被删掉", missing)
	}
}

// 没有控制台用户时只有 guard 那一个 —— 不许凭空造一个 gui 域目标出来。
func TestUninstallWaitTargetsFollowThePlan(t *testing.T) {
	plan := buildDarwinUninstallPlan(0, "", false)
	targets := bootoutWaitTargets(plan)
	if len(targets) != 1 || targets[0] != "system/"+darwinGuardLaunchdLabel {
		t.Errorf("没有控制台用户时要等的目标不对:%v", targets)
	}
}

// **只等 bootout,不等别的命令。** 计划里将来可能出现 bootstrap/kickstart,
// 对它们「等到消失」是把话说反了。
func TestUninstallNeverWaitsForANonBootoutCommand(t *testing.T) {
	plan := darwinUninstallPlan{LaunchctlCommands: [][]string{
		{"launchctl", "bootstrap", "system", "/Library/LaunchDaemons/x.plist"},
		{"launchctl", "kickstart", "-k", "system/x"},
	}}
	if targets := bootoutWaitTargets(plan); len(targets) != 0 {
		t.Errorf("对非 bootout 的命令也等了:%v", targets)
	}
}

// **等待必须排在删文件之前,而这条顺序是承重的。**
//
// 反过来就是这次 CI 抓到的形状:job 还在域里、文件已经没了,而 KeepAlive 让
// launchd 不停重拉一个不存在的二进制。顺序长在 AppKit 之外的普通 Go 里,但它
// 是**接线**而不是判据 —— 判据层的测试证明不了「谁排在谁前面」,这个仓库
// 全部的事故都在接线上。
func TestUninstallWaitsBeforeItDeletesTheFiles(t *testing.T) {
	src, err := os.ReadFile("uninstall_darwin.go")
	if err != nil {
		t.Fatalf("读不出 uninstall_darwin.go:%v —— 这条守卫读不懂现在的代码了,先修它", err)
	}
	body, ok := goFunctionBody(string(src), "func uninstallDarwinAction(c *urfavecli.Context) error {")
	if !ok {
		t.Fatal("读不出 uninstallDarwin —— 守卫的锚点漂了,先修它")
	}
	wait := strings.Index(body, "waitForLaunchdTargetsGone(")
	remove := strings.Index(body, "plan.RemovePaths")
	if wait < 0 {
		t.Fatal("卸载根本没等 launchd 拆完就往下走了 —— 正是 2026-09-15 CI 抓到的那个")
	}
	if remove < 0 {
		t.Fatal("找不到删文件那一段 —— 守卫读不懂现在的代码了")
	}
	if wait > remove {
		t.Error("等待排在了删文件之后 —— 那等于没等:文件已经没了,job 还在域里重拉它")
	}
}
