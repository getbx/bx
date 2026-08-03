package cli

import (
	"fmt"
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
