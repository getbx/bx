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
