package cli

import (
	"fmt"
	"os"
	"path/filepath"
)

// darwinUninstallPlan 是 darwin `bx uninstall` 的纯计划:哪些 launchctl 命令要跑、
// 哪些路径要删、哪些路径要保留(配置/数据,说明用)。无 build tag、无副作用,
// 便于在任意 GOOS 下单测。
type darwinUninstallPlan struct {
	LaunchctlCommands [][]string // bootout guard(+ 控制台用户在场时再 bootout gui menu)
	RemovePaths       []string   // plists、bridge、(统一布局下)runtime root + App、用户 agent plists
	KeepPaths         []string   // /etc/bx、/var/lib/bx(仅说明用,绝不出现在 RemovePaths)
}

const (
	darwinGuardLaunchdLabel      = "com.getbx.bx.guard"
	darwinGuardLaunchdPlistPath  = "/Library/LaunchDaemons/com.getbx.bx.guard.plist"
	darwinMenuLaunchdLabel       = "com.getbx.bx.menu"
	darwinLegacyMenuLaunchdLabel = "com.ggshr9.bx.menu"
	darwinBridgeBinPath          = "/usr/local/bin/bx"
	darwinRuntimeRootPath        = "/Library/Application Support/bx/runtime"
	darwinAppBundlePath          = "/Applications/Bx.app"
	darwinConfigDirPath          = "/etc/bx"
	darwinDataDirPath            = "/var/lib/bx"
)

// buildDarwinUninstallPlan 构造完整卸载计划。
//
// consoleUID<=0 或 consoleHome=="" 表示定位不到控制台用户(未登录桌面会话、或
// user.LookupId 失败):此时省略 gui 域的 launchctl bootout 与用户级文件
// (登录项 plist 在对方 home 下,无法安全定位/删除)。
//
// unifiedLayout=false(legacy 布局)时不删 runtime root 与 /Applications/Bx.app——
// 那两样只在统一 App 布局下才存在。
func buildDarwinUninstallPlan(consoleUID int, consoleHome string, unifiedLayout bool) darwinUninstallPlan {
	plan := darwinUninstallPlan{
		LaunchctlCommands: [][]string{
			{"launchctl", "bootout", "system/" + darwinGuardLaunchdLabel},
		},
		RemovePaths: []string{
			darwinGuardLaunchdPlistPath,
			darwinBridgeBinPath,
		},
		KeepPaths: []string{darwinConfigDirPath, darwinDataDirPath},
	}

	if unifiedLayout {
		plan.RemovePaths = append(plan.RemovePaths, darwinRuntimeRootPath, darwinAppBundlePath)
	}

	if consoleUID > 0 && consoleHome != "" {
		plan.LaunchctlCommands = append(plan.LaunchctlCommands,
			[]string{"launchctl", "bootout", fmt.Sprintf("gui/%d/%s", consoleUID, darwinMenuLaunchdLabel)},
		)
		agentDir := filepath.Join(consoleHome, "Library", "LaunchAgents")
		plan.RemovePaths = append(plan.RemovePaths,
			filepath.Join(agentDir, darwinMenuLaunchdLabel+".plist"),
			filepath.Join(agentDir, darwinLegacyMenuLaunchdLabel+".plist"),
		)
	}

	return plan
}

// unifiedTeardownNeeded 报告是否存在任一「统一 App 布局」产物路径(runtime root、
// /Applications/Bx.app)——用**存在性**而非健康度判定要不要走统一卸载分支。
//
// 不能用 unifiedLayoutActive()(靠 runtimedir.Installed 健康检查:current 链接
// 有效 + release.json 可解析 + bx 可执行文件存在)做这个决定:一次半途失败的
// app-install(SwitchCurrent 之前中断、release.json 损坏)会让健康检查判 false,
// 于是被误分类成 legacy 布局——runtime root 与 Bx.app 都不会被删,只有 bridge
// 被删,留下 root-owned 孤儿目录且违背「完整卸载」的承诺。存在性检查对损坏安装
// 也能正确触发清理。
func unifiedTeardownNeeded(paths ...string) bool {
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}
