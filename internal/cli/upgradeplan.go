package cli

import "fmt"

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
)

func upgradeSteps(guardianRunning bool, desiredOn bool) []UpgradeStep {
	if !guardianRunning {
		// 没有运行中的进程要换,也就没有断网的理由。
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
		return "升级需要重启保护,期间会断网几秒。现在继续吗?"
	}
	return "升级需要重启保护服务,当前保护未开启,不会影响网络。现在继续吗?"
}

// upgradeCannotAskMessage 是「没有终端可问」时给用户的那句话。
//
// 与 upgradeConfirmMessage 一样按 desiredOn 分叉,而且必须如此:升级要走确认
// 这条路的条件是「Guardian 在跑」,不是「保护开着」—— 而 Guardian 在跑、保护
// 关着,正是任何一次 bx down 之后的常态。对着这样一台机器说「会断网」,就是
// 在专门用来如实相告的地方再放一句不成立的话。
func upgradeCannotAskMessage(desiredOn bool) string {
	detail := "当前保护未开启,升级不会影响网络"
	if desiredOn {
		detail = "升级期间会断网几秒"
	}
	return fmt.Sprintf(
		"无法确认这次升级:当前不是交互式终端(%s)。"+
			"确认要升级请重跑并加 --yes(例如 ./install.sh --yes,或 bx app-install --yes)。", detail,
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
	return fmt.Sprintf(
		"! Guardian 仍在跑旧版 %s(已安装 %s)。执行 %s 完成切换(会断网几秒);"+
			"或打开 Bx.app 点 Install bx…(同一条路)。",
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
		// 破坏性步骤**全做完**再报告(记 desired=off、请 Core 退出、bootout
		// Guardian、删屏障阻断路由、还原系统 DNS)。保护已经被停过了。
		return fmt.Sprintf(
			"停止保护未能全部完成:%v\n"+
				"新版本文件尚未安装(升级没有开始),但保护已经被停过,网络是否已恢复未经确认——"+
				"请打开任意网页确认;若仍不通,执行 sudo bx uninstall(保留 /etc/bx 配置)后重新安装。", err,
		)
	default:
		if !networkRestored {
			return fmt.Sprintf(
				"升级未完成:%v\n"+
					"停止保护时走了强制拆除,网络是否已恢复未经确认——请打开任意网页确认。"+
					"若不通,执行 sudo bx uninstall 后重新安装。", err,
			)
		}
		// 走到这里说明已经干净地停过保护 —— 网络已还原为直连,可用。
		return fmt.Sprintf(
			"升级未完成:%v\n网络仍可正常使用(直连,无保护)。"+
				"若反复失败,执行 sudo bx uninstall 后重新安装。", err,
		)
	}
}
