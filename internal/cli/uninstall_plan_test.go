package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
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
