package cli

import (
	"fmt"

	"github.com/getbx/bx/internal/elevate"
)

// UpgradeStep 是升级流程中的一步。
//
// 抽成纯函数是因为顺序本身就是正确性:停保护必须早于装文件,而这一点只有在
// 它被单独测到时才不会在某次重构里被悄悄换掉(2026-08-08 真机事故的根因是
// 「换了符号链接但没重启进程」,同一类问题)。
type UpgradeStep int

const (
	// UpgradeStopProtection 停掉保护。放在最前面:它把网络还原成直连,
	// 于是其后任何一步失败,最差都只是「没有保护」而不是「断网」。
	UpgradeStopProtection UpgradeStep = iota
	UpgradeInstallFiles
	// UpgradeRestartGuardian 重启 daemon。仅仅换掉 runtime/current 符号链接
	// 是不够的 —— 一个已在跑的进程不会因此换代码。
	UpgradeRestartGuardian
	UpgradeStartProtection
	// UpgradeEnableGuardian 在**全新安装**之后把 Guardian 服务拉起来。
	//
	// 它不开启保护 —— Guardian 起来时 desired 还是 off,它只是开始服务那个
	// 控制 socket。但没有它,菜单栏一个**免密**的开关都点不动:菜单以普通用户
	// 身份连 /var/run/bx/guardian.sock,而全新安装只写了 plist、从没 bootstrap 过它,
	// socket 要等到第一次 `sudo bx up` 才存在。
	//
	// **真机实测(2026-08-10)**:装完 22:13:23、菜单 22:13:33 起来、
	// guardian.sock 到 22:14:57(`sudo bx up` 那一秒)才被创建。用户点
	// 「Start Protection」必然失败,而且三处都不留痕 —— Guardian 没起来所以没日志,
	// 菜单自己不记失败,403 按设计也不记(这次还不是 403,是根本连不上)。
	//
	// 升级路径一直没有这个问题:restartGuardianForUpgrade 会 bootout 再 bootstrap。
	// 也就是说菜单栏第①期的免密开关,**在全新安装这条路上从没能工作过**。
	UpgradeEnableGuardian
)

// configUsable 说的是「Guardian 起得来吗」,不是「配置内容对不对」:daemon 启动时
// 要读 /etc/bx/config.yaml 取 owner_uid,读不出或解析不了就直接退出 —— 而 plist 带
// KeepAlive=true,于是 bootstrap 一个读不到配置的 Guardian 等于制造一个崩溃循环。
// 所以「还没跑过 bx setup」的机器上这一步必须跳过,而不是硬拉。
func upgradeSteps(guardianRunning bool, desiredOn bool, configUsable bool) []UpgradeStep {
	if !guardianRunning {
		// 没有运行中的进程要换,也就没有断网的理由。
		// 但要把 Guardian 拉起来:否则菜单栏连不上那个 socket,一个免密开关都点不动。
		if configUsable {
			return []UpgradeStep{UpgradeInstallFiles, UpgradeEnableGuardian}
		}
		return []UpgradeStep{UpgradeInstallFiles}
	}
	steps := []UpgradeStep{UpgradeStopProtection, UpgradeInstallFiles, UpgradeRestartGuardian}
	if desiredOn {
		steps = append(steps, UpgradeStartProtection)
	}
	return steps
}

// upgradeConfirmMessage 明说会断网。
//
// 旧设计为了不打扰用户而绕开断网,代价是留下一个用户理解不了、Repair 也修不了的
// 中间态 —— 比直接断网难懂得多。含糊的「可能有短暂中断」同理:用户开着会议时
// 需要的是一个能据以决定「现在还是待会」的事实。
func upgradeConfirmMessage(desiredOn bool) string {
	if desiredOn {
		return "The upgrade has to restart protection, and the network drops for a few seconds. Continue now?"
	}
	return "The upgrade has to restart the protection service. Protection is off right now, so the network is unaffected. Continue now?"
}

// upgradeCannotAskMessage 是「没有终端可问」时给用户的那句话。
//
// 与 upgradeConfirmMessage 一样按 desiredOn 分叉,而且必须如此:升级要走确认
// 这条路的条件是「Guardian 在跑」,不是「保护开着」—— 而 Guardian 在跑、保护
// 关着,正是任何一次 bx down 之后的常态。对着这样一台机器说「会断网」,就是
// 在专门用来如实相告的地方再放一句不成立的话。
func upgradeCannotAskMessage(desiredOn bool) string {
	detail := "protection is off right now, so the upgrade does not affect the network"
	if desiredOn {
		detail = "the network drops for a few seconds during the upgrade"
	}
	return fmt.Sprintf(
		"this upgrade could not be confirmed: this is not an interactive terminal (%s). "+
			"To confirm the upgrade, run it again with --yes (./install.sh --yes, say, or bx app-install --yes).", detail,
	)
}

// upgradeSwitchCommand 是「把 Guardian 切到已装好的新版」在终端里**真的能跑通**
// 的那条命令。
//
// 不能写成 `sudo bx app-install`:/usr/local/bin/bx 是 bridge,它 syscall.Exec 到
// /Library/Application Support/bx/runtime/<version>/bx,于是 appInstallAction 用
// os.Executable() 反推 --app-source 时拿到的是 runtime 路径 —— 那里没有
// /Bx.app/Contents/Resources/,bundleRootFromExecutable 直接报
// "is not inside a Bx.app bundle"。一条抄下来必然失败的命令,比不给命令更糟。
// 从 App 包里的 bx-cli 直接跑就没有这一跳:它自己就在 Bx.app 里,--app-source
// 自动推成 /Applications/Bx.app,而 installAppBundle 认得「源与目的地是同一个」
// (samePath)并跳过整树复制,其余步骤(runtime/bridge/Guardian plist/重启/恢复
// 保护)照常做完 —— 这正是「完成切换」需要的。unifiedlayout.go 的
// unifiedRepairHint 早就用的是这一条,两处保持一致。
const upgradeSwitchCommand = "sudo " + darwinAppBundlePath + "/Contents/Resources/bx-cli app-install"

// upVersionMismatchMessage 在 Guardian 跑着旧版时给出提示。
//
// 2026-08-08:`bx up` 能看到 runtime 是新版而自己是旧版,却照常报 Protected ——
// 「信念 vs 事实」在这里又演了一遍。任一版本为空说明信息不全,不猜。
func upVersionMismatchMessage(guardianVersion, runtimeVersion string) string {
	if guardianVersion == "" || runtimeVersion == "" || guardianVersion == runtimeVersion {
		return ""
	}
	// 刻意只给这一条命令。曾追加过「或打开 Bx.app 点 Install bx…」,而菜单只在
	// .notInstalled 状态才有那一项 —— 那个状态与「检测到版本不一致」互斥(能检测到
	// 不一致,说明 runtime 装着且 CLI 可用,此时菜单是 .connected/.warning)。
	// 给一条指向不存在菜单项的指引,与本函数要消灭的那类假话同级。
	return fmt.Sprintf(
		"! Guardian is still running the old version %s (%s is installed). Run %s to finish the switch (the network drops for a few seconds).",
		guardianVersion, runtimeVersion, upgradeSwitchCommand,
	)
}

func upgradeFailureMessage(step UpgradeStep, err error) string {
	return upgradeFailureMessageWithNetwork(step, err, true)
}

// upgradeFailureMessageWithNetwork 在失败时如实说清「现在处于什么状态」。
//
// networkRestored=false 表示停保护那一步走了强制拆除(`bx down` 的逃生路径)。
// 那条路是 best-effort,macOSDownAction 自己都只列举做过的动作、明确拒绝断言
// 「网络已还原」;在这里替它断言「网络仍可正常使用」,就是把一句它不敢说的话
// 说给一个可能正断着网的用户听。
func upgradeFailureMessageWithNetwork(step UpgradeStep, err error, networkRestored bool) string {
	switch step {
	case UpgradeStopProtection:
		// 「当前状态未变」是假的:macOSDownLifecycleDetailed 只会在
		// forcedMacOSTeardown 报错时返回错误,而那条逃生路径按设计会把六个
		// 破坏性步骤**全做完**再报告(记下停机意图 —— 升级记维护挂起、用户记
		// desired=off、请 Core 退出、bootout Guardian、删屏障阻断路由、还原
		// 系统 DNS)。保护已经被停过了。
		return fmt.Sprintf(
			"stopping protection did not finish every step: %v\n"+
				"the new version's files are not installed (the upgrade never started), but protection was stopped, and whether the network is back has not been confirmed — "+
				"open any web page to check; if it is still not working, run "+elevate.Prefix+"bx uninstall (it keeps /etc/bx), then install again.", err,
		)
	case UpgradeEnableGuardian:
		// 这一步只在全新安装那条路上出现:文件都装好了、保护从没开过、网络一直是
		// 直连。**别套用「升级未完成」那套话** —— 这台机器上没有任何东西被停过。
		// 失败的后果是具体且有限的:菜单栏的开关点不动(它连的 socket 不存在),
		// 而命令行的 sudo bx up 自己会再拉一次 Guardian,照样能开起来。
		return fmt.Sprintf(
			"the new version's files are installed, but the protection service did not start: %v\n"+
				"The network is unaffected (it has been direct all along, since protection was never on). The switch in the menu bar will not work for now — "+
				""+elevate.Prefix+"bx up turns protection on and brings the service up with it; if that still fails, see sudo tail -50 /var/log/bx-guard.err.log.", err,
		)
	default:
		if !networkRestored {
			return fmt.Sprintf(
				"the upgrade did not finish: %v\n"+
					"stopping protection went through the forced teardown, so whether the network is back has not been confirmed — open any web page to check. "+
					"If it is not, run "+elevate.Prefix+"bx uninstall, then install again.", err,
			)
		}
		// 走到这里说明已经干净地停过保护 —— 网络已还原为直连,可用。
		return fmt.Sprintf(
			"the upgrade did not finish: %v\nThe network still works (direct, unprotected). "+
				"If it keeps failing, run "+elevate.Prefix+"bx uninstall, then install again.", err,
		)
	}
}
