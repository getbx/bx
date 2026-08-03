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
	plan := buildDarwinUninstallPlan(consoleUID, consoleHome, unifiedLayoutActive())

	for _, args := range plan.LaunchctlCommands {
		cmd := exec.Command(args[0], args[1:]...)
		if err := cmd.Run(); err != nil {
			fmt.Printf("! %s: %v\n", strings.Join(args, " "), err)
		}
	}

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
