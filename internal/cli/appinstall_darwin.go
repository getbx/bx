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
	configPath := c.String("config")

	outcome, err := runUpgrade(upgradeIO{
		guardianRunning: func() (bool, error) { return install.GuardianLoaded(c.Context) },
		loadDesiredOn:   func() bool { return upgradeDesiredOn(c.Context) },
		saveIntent:      saveUpgradeIntent,
		clearIntent:     clearUpgradeIntent,
		confirm:         confirmOnTTY,
		stopProtection: func() (macOSDownResult, error) {
			// downPurposeUpgrade:这一跳把「停下来换二进制」记成一次**维护
			// 挂起**,而不是把 desired 改写成 off —— 用户想要保护,只是此刻
			// 不能有,而磁盘上那句「用户不想要保护」会被任何忠实的调谐器照办。
			// 它同时保住上一行刚写下的升级欠条:用普通的 Down,Guardian 会把
			// 这一跳当作「用户不要保护了」立刻销掉挂起与欠条,于是装文件一失败,
			// 重试既读到 desired=off 又找不到欠条,「成功」地把机器永久留在
			// 无保护状态(2026-08-08 复审 C1)。
			return macOSDownLifecycleFor(c.Context, downPurposeUpgrade, configPath, defaultMacOSLifecycleDeps())
		},
		installFiles: func() (installedFiles, error) {
			result, err := install.UnifiedInstall(install.UnifiedInstallOptions{
				BundlePath:  source,
				ConfigPath:  configPath,
				ConsoleHome: account.HomeDir,
				ConsoleUID:  uid,
				ConsoleGID:  gid,
			})
			return installedFiles{
				Version:           result.Version,
				AppPath:           result.AppPath,
				RuntimeExecutable: result.RuntimeExecutable,
			}, err
		},
		restartGuardian: func() error { return restartGuardianForUpgrade(c.Context) },
		startProtection: func() error {
			_, err := macOSUpLifecycle(c.Context, configPath, defaultMacOSLifecycleDeps())
			return err
		},
		log: func(line string) { fmt.Println(line) },
	}, c.Bool("yes"))
	if outcome.ForcedTeardown {
		// bx down 走逃生路径时会如实告知,这里不能把它吞掉:那条路是 best-effort,
		// 可能没能真正让网络恢复。
		//
		// 原因**必须**由 forcedTeardownReason 导出,不能在这里再抄一份:强制路径
		// 的进法不止两种,而 macOSDownLifecycleFor 选 legacy 分支时根本不看
		// purpose —— 升级路径照样会走到它。内联的副本就是这么把「Guardian 未响应」
		// 打印在一个明明应答了的 Guardian 头上的。
		fmt.Fprintf(os.Stderr, "⚠️  %s;请自行确认网络是否恢复。\n", forcedTeardownReason(outcome.Down))
	}
	if err != nil {
		return err
	}
	if outcome.Cancelled {
		// 退出码 2 = 用户明确说了不。不是失败(没有任何东西坏掉),但也不是
		// 成功——调用它的 install.sh 据此不再打印「完成」,而是如实说取消了。
		return urfavecli.Exit("已取消,未做任何改动。", 2)
	}

	fmt.Printf("✓ 已安装 Bx.app %s → %s\n", outcome.Files.Version, outcome.Files.AppPath)
	fmt.Printf("✓ runtime → %s\n", outcome.Files.RuntimeExecutable)
	fmt.Println("✓ 命令行入口 /usr/local/bin/bx 与 Guardian 服务已就绪")
	// 安装刚改过 plist(可执行路径可能变了),必须强制重载才能生效。
	if err := ensureMacOSMenuReloaded(uid, true); err != nil {
		fmt.Printf("! 菜单栏未能自动启动(可手动打开 Bx.app): %v\n", err)
	}
	// 收尾文案由**做过的事**导出,不由「打算做什么」导出。
	fmt.Println(appInstallNextStep(outcome.ProtectionRestored, configured(configPath)))
	return nil
}

// appInstallNextStep 挑一句收尾。
//
// 「下一步:sudo bx setup <client-link>」只对从没配过的机器成立;对一台刚升级完
// 的机器说这句,等于让用户去重做一遍他早就做过的事。
func appInstallNextStep(protectionRestored, configured bool) string {
	switch {
	case protectionRestored:
		return "升级完成,保护已按升级前的状态恢复。"
	case configured:
		return "安装完成(未启动保护)。开启保护:sudo bx up,或用菜单栏的开关。"
	default:
		return "下一步:菜单栏 Set Up bx,或 sudo bx setup <client-link> && sudo bx up"
	}
}

func configured(configPath string) bool {
	_, err := os.Stat(configPath)
	return err == nil
}

// upgradeDesiredOn 回答「这台机器此刻想不想开着保护」,决定升级末尾要不要把
// 保护起回来。
//
// 两个来源合成(resolveUpgradeDesiredOn):
//   - 上一次未完成升级留下的欠条(guardian.Store 的 UpgradeIntent)。**没有它就修不好重试**:
//     第一步会把 desired 写成 off,重试时读 store 只会读到 off。
//   - Guardian 自己的 desired store(/var/lib/bx/guardian-state.json):那正是
//     Guardian 开机时据以决定要不要起保护的状态,也是 bx down 写 off 的地方。
//     文件不存在按 off(Store.LoadDesired 的语义),即从未装过/从未开过。
//
// 只有 store **读失败**(损坏、不可读)时才退而问运行中的 Guardian 控制 socket:
// 一个正在 Protected 的实例,其意图必然是 on。都问不出来就当 off —— 宁可让用户
// 自己 sudo bx up,也不要在不知情的情况下替他把保护打开。
//
// 调用点必须早于第一步:UpgradeStopProtection 无论走干净路径还是强制拆除,都会
// 把 desired 记成 off。
func upgradeDesiredOn(ctx context.Context) bool {
	pending, present := loadUpgradeIntent()
	return resolveUpgradeDesiredOn(guardianDesiredOn(ctx), pending, present)
}

func guardianDesiredOn(ctx context.Context) bool {
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
