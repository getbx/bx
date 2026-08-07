//go:build darwin

package cli

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"strconv"

	"github.com/getbx/bx/internal/install"
	urfavecli "github.com/urfave/cli/v2"
)

func appInstallAction(c *urfavecli.Context) error {
	if os.Geteuid() != 0 {
		return errors.New("app-install 需要 root:请通过 Bx.app 的 Install bx 或 sudo 执行")
	}
	source := c.String("app-source")
	if source == "" {
		executable, err := os.Executable()
		if err != nil {
			return err
		}
		if source, err = bundleRootFromExecutable(executable); err != nil {
			return err
		}
	}
	uid, err := consoleUserUID()
	if err != nil {
		return fmt.Errorf("定位控制台用户失败: %w", err)
	}
	account, err := user.LookupId(strconv.Itoa(uid))
	if err != nil {
		return fmt.Errorf("查找控制台用户失败: %w", err)
	}
	gid, err := strconv.Atoi(account.Gid)
	if err != nil {
		return err
	}
	result, err := install.UnifiedInstall(install.UnifiedInstallOptions{
		BundlePath:  source,
		ConfigPath:  c.String("config"),
		ConsoleHome: account.HomeDir,
		ConsoleUID:  uid,
		ConsoleGID:  gid,
	})
	if err != nil {
		return err
	}
	fmt.Printf("✓ 已安装 Bx.app %s → %s\n", result.Version, result.AppPath)
	fmt.Printf("✓ runtime → %s\n", result.RuntimeExecutable)
	fmt.Println("✓ 命令行入口 /usr/local/bin/bx 与 Guardian 服务已就绪(未启动保护)")
	if install.GuardianActive() {
		fmt.Println("! 检测到 Guardian 正在运行:旧进程仍按旧版运行,建议尽快执行 sudo bx down && sudo bx up 完成切换")
	}
	// 安装刚改过 plist(可执行路径可能变了),必须强制重载才能生效。
	if err := ensureMacOSMenuReloaded(uid, true); err != nil {
		fmt.Printf("! 菜单栏未能自动启动(可手动打开 Bx.app): %v\n", err)
	}
	fmt.Println("下一步:菜单栏 Set Up bx,或 sudo bx setup <client-link> && sudo bx up")
	return nil
}
