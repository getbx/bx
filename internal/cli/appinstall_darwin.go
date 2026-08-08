//go:build darwin

package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/user"
	"strconv"

	"github.com/getbx/bx/internal/guardian"
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

	// 意图必须在动手之前读完:第一步(停保护)会把 desired 写成 off,之后再读
	// 就永远是 off,保护再也回不来。
	guardianRunning := install.GuardianActive()
	desiredOn := upgradeDesiredOn(c.Context)
	steps := upgradeSteps(guardianRunning, desiredOn)

	// 只有确实要动运行中的 Guardian 时才问 —— 全新安装什么都不会中断。
	if guardianRunning && !c.Bool("yes") {
		if !confirmOnTTY(upgradeConfirmMessage(desiredOn)) {
			fmt.Println("已取消,未做任何改动。")
			return nil
		}
	}

	var result install.UnifiedInstallResult
	for _, step := range steps {
		var stepErr error
		switch step {
		case UpgradeStopProtection:
			fmt.Println("• 停止保护(网络将暂时回到直连)")
			_, stepErr = macOSDownLifecycleDetailed(c.Context, c.String("config"), defaultMacOSLifecycleDeps())
		case UpgradeInstallFiles:
			fmt.Println("• 安装新版本文件")
			result, stepErr = install.UnifiedInstall(install.UnifiedInstallOptions{
				BundlePath:  source,
				ConfigPath:  c.String("config"),
				ConsoleHome: account.HomeDir,
				ConsoleUID:  uid,
				ConsoleGID:  gid,
			})
		case UpgradeRestartGuardian:
			fmt.Println("• 重启保护服务(使新版本生效)")
			stepErr = restartGuardianForUpgrade(c.Context)
		case UpgradeStartProtection:
			fmt.Println("• 恢复保护")
			_, stepErr = macOSUpLifecycle(c.Context, c.String("config"), defaultMacOSLifecycleDeps())
		}
		if stepErr != nil {
			return errors.New(upgradeFailureMessage(step, stepErr))
		}
	}

	fmt.Printf("✓ 已安装 Bx.app %s → %s\n", result.Version, result.AppPath)
	fmt.Printf("✓ runtime → %s\n", result.RuntimeExecutable)
	fmt.Println("✓ 命令行入口 /usr/local/bin/bx 与 Guardian 服务已就绪")
	// 安装刚改过 plist(可执行路径可能变了),必须强制重载才能生效。
	if err := ensureMacOSMenuReloaded(uid, true); err != nil {
		fmt.Printf("! 菜单栏未能自动启动(可手动打开 Bx.app): %v\n", err)
	}
	if desiredOn {
		fmt.Println("升级完成,保护已按升级前的状态恢复。")
		return nil
	}
	fmt.Println("下一步:菜单栏 Set Up bx,或 sudo bx setup <client-link> && sudo bx up")
	return nil
}

// upgradeDesiredOn 回答「这台机器此刻想不想开着保护」,决定升级末尾要不要把
// 保护起回来。
//
// 取自 Guardian 自己的 desired store(/var/lib/bx/guardian-state.json):那正是
// Guardian 开机时据以决定要不要起保护的状态,也是 bx down 写 off 的地方 ——
// 用同一份状态才不会和 Guardian 的判断分家。文件不存在按 off(Store.LoadDesired
// 的语义),即从未装过/从未开过。
//
// 只有读**失败**(损坏、不可读)时才退而问运行中的 Guardian 控制 socket:一个
// 正在 Protected 的实例,其意图必然是 on。两处都问不出来就当 off —— 宁可让用户
// 自己 sudo bx up,也不要在不知情的情况下替他把保护打开。
//
// 调用点必须早于第一步:UpgradeStopProtection 无论走干净路径还是强制拆除,都会
// 把 desired 记成 off。
func upgradeDesiredOn(ctx context.Context) bool {
	if desired, err := guardian.OpenDefaultStore().LoadDesired(); err == nil {
		return desired == guardian.DesiredOn
	}
	status, err := guardian.NewClient(guardian.SocketPath).Status(ctx)
	if err != nil {
		return false
	}
	return status.Desired == guardian.DesiredOn || status.Protection == guardian.ProtectionProtected
}

// restartGuardianForUpgrade 把旧 daemon 停掉再拉起来 —— 换掉 runtime/current
// 符号链接不会让一个已在跑的进程换代码,这正是 2026-08-08 事故的根因。
//
// bootout 之后必须显式 bootstrap:bootout 是把 job 从 launchd 卸掉,plist 里的
// KeepAlive 只对还在 launchd 里的 job 有效,不会把它拉回来。desiredOn=true 时
// 后面的 macOSUpLifecycle 经 ensureGuardianOwnership → install.EnableGuardian
// 也会 bootstrap(幂等,active&&ready 时不发任何 launchctl 命令);但
// desiredOn=false 时没有那一步,而「Guardian 在跑、保护未开启」正是任何一次
// bx down 之后的常态 —— 升级不该顺手把它变成「Guardian 根本没在跑」。
func restartGuardianForUpgrade(ctx context.Context) error {
	if err := install.BootoutGuardian(ctx); err != nil {
		return err
	}
	return install.EnableGuardian()
}
