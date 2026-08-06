package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// darwinUninstallPlan 是 darwin `bx uninstall` 的纯计划:哪些 launchctl 命令要跑、
// 哪些路径要删、哪些路径要保留(配置/数据,说明用)。无 build tag、无副作用,
// 便于在任意 GOOS 下单测。
type darwinUninstallPlan struct {
	LaunchctlCommands [][]string // bootout guard(+ 控制台用户在场时再 bootout gui menu)
	RemovePaths       []string   // plists、bridge、/var/lib/bx 下的运行时状态文件(逐个,非整目录)、(统一布局下)runtime root + App、用户 agent plists
	KeepPaths         []string   // /etc/bx、/var/lib/bx(仅说明用,绝不出现在 RemovePaths;目录本身与其余内容如 brook/sing-box/china 列表保留)
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

	// 运行时状态文件,与 darwinDataDirPath 下的用户数据(brook/sing-box 二进制、
	// china 列表、/etc/bx 配置)不同命运:卸载必须清掉,否则陈旧记录(如指向
	// 已死 PID 的 core-process.json)会活过卸载重装,卡死下一次 `bx up`
	// (真机事故,2026-08-05)。逐个文件加入 RemovePaths,绝不整目录删
	// darwinDataDirPath——那会连同内嵌释放的 brook/sing-box 二进制、china
	// 列表一起删掉。
	darwinCoreProcessStatePath = darwinDataDirPath + "/core-process.json"
	darwinGuardianStatePath    = darwinDataDirPath + "/guardian-state.json"
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
			darwinCoreProcessStatePath,
			darwinGuardianStatePath,
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

// asuserBootoutFallback 为一条失败的 `launchctl bootout gui/<uid>/<label>` 构造
// 重试命令 `launchctl asuser <uid> launchctl bootout gui/<uid>/<label>`;不适用
// 时返回 nil(非 bootout、非 gui 域、域串不含 uid、或本身已经是 asuser 重试)。
//
// 为什么需要:菜单栏 LaunchAgent 现在带 KeepAlive={SuccessfulExit:false}。此前
// bootout 失败是良性的——只有 RunAtLoad 的 job 不会自己回来;现在一旦 bootout
// 失败,job 仍留在 launchd 里,而紧接着的 RemovePaths 会把 /Applications/Bx.app
// 从它脚下删掉,于是 launchd 每 ~10s 重拉一个已不存在的二进制、刷屏
// menu.err.log,直到用户注销——而用户以为 bx 已经卸干净了。
//
// 重试形式照抄 menuBootstrapCommand 的既有修复:root 没有目标用户 GUI 域的
// audit session token,`launchctl asuser <uid>` 先切进那个会话再发真命令。
// root 对 gui 域的 bootout 究竟会不会失败并未真机坐实(CLAUDE.md 记的是
// 「真机只坐实了 bootstrap 一步,其余未验证」),故这是**兜底**而非替换:
// 先按原样直发,失败了才走 asuser 重试。
//
// 调用方必须保持整条路径 best-effort:重试再失败也只警告、继续卸载,绝不中止。
// 「停止」永远不许依赖先成功做成别的什么事。
func asuserBootoutFallback(args []string) []string {
	if len(args) != 3 || args[1] != "bootout" {
		return nil
	}
	domainTarget := args[2]
	rest, ok := strings.CutPrefix(domainTarget, "gui/")
	if !ok {
		return nil
	}
	uid, _, ok := strings.Cut(rest, "/")
	if !ok || uid == "" {
		return nil
	}
	if _, err := strconv.Atoi(uid); err != nil {
		return nil
	}
	return []string{args[0], "asuser", uid, "launchctl", "bootout", domainTarget}
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
