//go:build darwin

package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"strings"
	"time"

	"github.com/getbx/bx/internal/guardian"
	"github.com/getbx/bx/internal/install"
	urfavecli "github.com/urfave/cli/v2"
)

// uninstallDarwinAction 是 darwin `bx uninstall` 的完整卸载执行器:守护进程状态
// 校验 + 计划执行(plists、bridge、统一 runtime、App、登录项),保留配置/数据。
func uninstallDarwinAction(c *urfavecli.Context) error {
	if os.Geteuid() != 0 {
		return errors.New("卸载需要 root:请用 sudo bx uninstall")
	}

	if err := ensureGuardianNotRunningForUninstall(); err != nil {
		return err
	}

	consoleUID, consoleHome := darwinConsoleUserForUninstall()
	unifiedLayout := unifiedTeardownNeeded(darwinRuntimeRootPath, darwinAppBundlePath)
	plan := buildDarwinUninstallPlan(consoleUID, consoleHome, unifiedLayout)

	for _, args := range plan.LaunchctlCommands {
		runLaunchctlBestEffort(args)
	}
	// **等 launchd 真的把 job 拆掉,再往下删它的文件。**
	// 顺序是承重的:下面 RemovePaths 会删掉 plist、二进制与 App bundle,而 guard
	// 与菜单 agent 都带 KeepAlive —— job 还在域里时把文件删掉,launchd 会不停
	// 重拉一个不存在的二进制,而卸载已经报了成功。
	waitForLaunchdTargetsGone(bootoutWaitTargets(plan))

	if err := install.Uninstall(); err != nil {
		fmt.Printf("! 清理 legacy 服务失败: %v\n", err)
	}

	for _, path := range plan.RemovePaths {
		if err := os.RemoveAll(path); err != nil {
			fmt.Printf("! 删除 %s 失败: %v\n", path, err)
			continue
		}
		fmt.Printf("✓ 已删除 %s\n", path)
	}

	fmt.Println("已卸载 bx。以下路径已保留(如需彻底清除配置数据请手动删除):")
	for _, path := range plan.KeepPaths {
		fmt.Printf("  %s\n", path)
	}
	return nil
}

// runLaunchctlBestEffort 执行一条卸载计划里的 launchctl 命令。失败时,若它是
// gui 域的 bootout(菜单栏 LaunchAgent),先用 `launchctl asuser <uid>` 重试一次
// 再放弃——root 没有目标用户 GUI 域的 session token,这与 menuBootstrapCommand
// 已修过的那类失败同源。
//
// 之所以值得多这一跳:菜单栏 agent 现在带 KeepAlive,bootout 失败 = job 留在
// launchd 里,而后续步骤会把 App bundle 删掉,launchd 便会不停重拉一个不存在的
// 二进制,直到用户注销。
//
// 全程 best-effort:重试失败也只打警告继续,卸载绝不因此中止——「停止」不许依赖
// 先成功做成别的事。
func runLaunchctlBestEffort(args []string) {
	err := exec.Command(args[0], args[1:]...).Run()
	if err == nil {
		return
	}
	if launchctlBootoutAlreadyGone(args, commandExitCode(err)) {
		return
	}
	fallback := asuserBootoutFallback(args)
	if fallback == nil {
		fmt.Printf("! %s: %v\n", strings.Join(args, " "), err)
		return
	}
	retryErr := exec.Command(fallback[0], fallback[1:]...).Run()
	if retryErr == nil {
		return
	}
	if launchctlBootoutAlreadyGone(fallback, commandExitCode(retryErr)) {
		return
	}
	fmt.Printf("! %s: %v(已重试 %s: %v)\n",
		strings.Join(args, " "), err, strings.Join(fallback, " "), retryErr)
}

// commandExitCode 取出命令的退出码;命令根本没跑起来(找不到可执行文件等)返回 -1。
func commandExitCode(err error) int {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

// ensureGuardianNotRunningForUninstall 拒绝在保护仍在运行时卸载:Guardian 不可达
// 视为未运行(继续卸载),可达且处于活跃态之一则要求先 `sudo bx down`。
func ensureGuardianNotRunningForUninstall() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	client := guardian.NewClient(guardian.SocketPath)
	status, err := client.Status(ctx)
	if err != nil {
		return nil
	}
	switch status.Protection {
	case guardian.ProtectionProtected, guardian.ProtectionStarting, guardian.ProtectionRecovering, guardian.ProtectionBlocked:
		return errors.New("保护仍在运行:先执行 sudo bx down 再卸载")
	}
	return nil
}

// darwinConsoleUserForUninstall 定位控制台用户的 uid/home,供计划构造用户级
// 登录项清理。定位不到时返回 (0, ""),计划构造器据此跳过 gui 域与用户文件。
func darwinConsoleUserForUninstall() (int, string) {
	uid, err := consoleUserUID()
	if err != nil {
		fmt.Printf("! 未找到控制台用户,跳过登录项与用户级文件清理: %v\n", err)
		return 0, ""
	}
	account, err := user.LookupId(strconv.Itoa(uid))
	if err != nil {
		fmt.Printf("! 无法定位控制台用户主目录,跳过登录项与用户级文件清理: %v\n", err)
		return 0, ""
	}
	return uid, account.HomeDir
}

// launchdTeardownPollInterval / launchdTeardownPollAttempts 界定「等 job 真的
// 从域里消失」等多久。取值与 install.bootoutGuardian 那条路同量级(约 3 秒上限):
// 真机上 launchd 拆一个 KeepAlive 服务通常几百毫秒。
const (
	launchdTeardownPollInterval = 150 * time.Millisecond
	launchdTeardownPollAttempts = 20
)

// waitForLaunchdTargetsGone 等每个 bootout 过的目标真的不在域里。
//
// **等不到不报错、也不中止卸载** —— 停止这条路上任何一步都不得因为别的事没做成
// 而失败(2026-08-04 那次 71 分钟事故立的规矩)。等不到时打一行,让用户知道
// 可能要注销一次;而**沉默才是最坏的**:那正是「用户以为卸干净了」的来源。
func waitForLaunchdTargetsGone(targets []string) {
	for _, target := range targets {
		gone := false
		for i := 0; i < launchdTeardownPollAttempts; i++ {
			if exec.Command("launchctl", "print", target).Run() != nil {
				gone = true
				break
			}
			time.Sleep(launchdTeardownPollInterval)
		}
		if !gone {
			fmt.Printf("! launchd 里的 %s 还没拆掉(已等约 %s)—— 它带 KeepAlive,"+
				"可能会重拉一个已被删掉的二进制,注销一次即可清掉\n",
				target, launchdTeardownPollInterval*launchdTeardownPollAttempts)
		}
	}
}
