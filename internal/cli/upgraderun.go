package cli

import (
	"errors"
	"fmt"
)

// installedFiles 是「装文件」这一步的产物,只留展示需要的三项。
//
// 刻意不用 install.UnifiedInstallResult:那是 darwin-only 类型,用了会把整个
// 编排连同它的测试一起锁进 darwin。
type installedFiles struct {
	Version           string
	AppPath           string
	RuntimeExecutable string
}

// upgradeIO 是编排里所有会碰到系统的动作。
//
// 抽出来是为了让**顺序本身**可测:「desired 必须在停保护之前读完」「欠条必须在
// 停保护之前落盘」这两条正确性此前只由注释保证 —— 把它们换成常量,整套测试照样
// 全绿(复审实测)。现在它们由 upgraderun_test.go 里的调用序列钉住。
type upgradeIO struct {
	// guardianRunning 报告 Guardian 是否在跑。返回 error 表示**问不出来**,
	// 由 runUpgrade 按「在跑」处理(fail-closed),绝不压成 false。
	guardianRunning func() (bool, error)
	// loadDesiredOn 回答「升级前这台机器想不想开着保护」。
	loadDesiredOn func() bool
	// saveIntent 把上面这个答案落盘,供中途失败后的重试读取。
	saveIntent func(desiredOn bool) error
	// clearIntent 抹掉欠条(升级完整做完,或本来就没欠)。
	clearIntent func() error
	// confirm 询问用户。返回 (同意, 错误):false+nil 是「用户说不」,
	// 非 nil error 是「问不出来」—— 后者必须把整条命令带成非零退出,否则调用它的
	// 脚本会在 set -e 下继续往下走、把「什么都没做」打印成「完成」。
	confirm func(prompt string) (bool, error)
	// stopProtection 停保护,返回 bx down 那条路自己的结果。整个 result 一起
	// 带回来(而不是拆成 forced+cause 两个值)是有理由的:强制路径的**原因**不止
	// 两种,收尾文案要靠 forcedTeardownReason 区分,少带一个字段就会打印出假话。
	// 那条路是 best-effort,bx down 自己都拒绝断言「网络已还原」。
	stopProtection  func() (macOSDownResult, error)
	installFiles    func() (installedFiles, error)
	restartGuardian func() error
	startProtection func() error
	log             func(string)
}

// upgradeOutcome 描述这次 app-install 实际做了什么,供调用方如实汇报 ——
// 收尾文案必须由**做过的事**导出,不能由「打算做什么」导出。
type upgradeOutcome struct {
	Files              installedFiles
	Cancelled          bool
	ProtectionRestored bool
	// Down 是停保护那一步的完整结果 —— 收尾文案必须能区分强制路径的每一种
	// 原因(见 forcedTeardownReason),所以这里不把它拆扁。
	Down             macOSDownResult
	ForcedTeardown   bool
	IntentLeftBehind bool
}

func runUpgrade(io upgradeIO, assumeYes bool) (upgradeOutcome, error) {
	var outcome upgradeOutcome

	// 「问不出来」当作「在跑」。install.GuardianActive() 是 `err == nil && active`,
	// 探测一失败就压成 false,于是计划退化成「只装文件」—— 文件在活着的旧
	// Guardian 底下被换掉,正是本期要消灭的那个 bug,而且还会报「升级完成」。
	running, err := io.guardianRunning()
	if err != nil {
		running = true
		io.log(fmt.Sprintf("! 无法确认 Guardian 是否在运行(%v):按「正在运行」处理,会停保护并重启服务", err))
	}

	// 意图必须在动手之前读完并落盘。
	desiredOn := io.loadDesiredOn()
	steps := upgradeSteps(running, desiredOn)
	stopsProtection := stepsContain(steps, UpgradeStopProtection)

	if stopsProtection && !assumeYes {
		agreed, err := io.confirm(upgradeConfirmMessage(desiredOn))
		if errors.Is(err, errCannotAsk) {
			// 问不出来 ≠ 用户说不。必须报错退出:静静地 return nil 会让
			// install.sh(set -e)紧接着打印「完成」,而旧 daemon 还跑着旧代码。
			// 文案按 desiredOn 生成,不对一台保护本就关着的机器声称会断网。
			return outcome, errors.New(upgradeCannotAskMessage(desiredOn))
		}
		if err != nil {
			return outcome, err
		}
		if !agreed {
			outcome.Cancelled = true
			return outcome, nil
		}
	}
	if stopsProtection && desiredOn {
		if err := io.saveIntent(true); err != nil {
			// 记不下欠条就不要开始:一旦中途失败,没人知道该把保护起回来。
			// 此刻一步都没做,这句「状态未变」是真的。
			return outcome, fmt.Errorf("记录升级前的保护状态失败,升级未开始,当前状态未变:%w", err)
		}
	}

	networkRestored := true
	for _, step := range steps {
		var stepErr error
		switch step {
		case UpgradeStopProtection:
			io.log("• 停止保护(网络将暂时回到直连)")
			down, err := io.stopProtection()
			outcome.Down = down
			outcome.ForcedTeardown = down.Forced || err != nil
			if outcome.ForcedTeardown {
				networkRestored = false
			}
			stepErr = err
			// **Guardian 没能确认保护关掉时,不许继续换二进制。**
			//
			// 这条以前漏了:判断只看 down.Forced 与 err,而「200 但
			// protection_state != off」两者都不满足 —— 于是升级会照常换掉 runtime
			// 二进制、重启 Guardian,而一个不受管的 Core 还在跑着老二进制、占着 TUN。
			// 症状要到之后才现:startProtection 撞上 latch 住的
			// core_ownership_uncertain,而按 CLAUDE.md 那个状态只有 down+up 能解 ——
			// 一个远比现在中止更难懂的失败。
			//
			// 中止是安全的:这是第一步,文件还一个字节都没动。
			//
			// **只在「确知还有 Core 在跑」时中止。** 「问不出来」(扫描失败/不支持)
			// 也让 downConfirmedStopped 为假,但那种情况用户做什么都清不掉:重跑升级
			// 会撞上同一道闸门,而菜单的 Repair 走的正是 app-install —— 也就是说
			// 送修复的通道自己被堵死了。那时警告并继续,方向与「停止不许依赖别的
			// 先成功」一致:不确定不该变成寸步难行。
			if stepErr == nil && !downConfirmedStopped(down) {
				if reason := downUnconfirmedReason(down); reason == "core_still_running" {
					stepErr = fmt.Errorf(
						"Guardian 确认系统里仍有 bx 的 Core 进程在跑(%s):此时换掉二进制会留下一个"+
							"不受管的旧 Core 占着 TUN,而症状要到之后才现(core_ownership_uncertain,"+
							"只有 sudo bx down 再 sudo bx up 能解)。"+
							"先看 sudo tail -50 /var/log/bx-guard.err.log 确认是哪个进程并处理掉,再重跑升级",
						reason,
					)
				} else {
					io.log(fmt.Sprintf(
						"! Guardian 没能确认保护已经关闭(%s):继续升级,但升级后若起不来保护,"+
							"先看 sudo tail -50 /var/log/bx-guard.err.log", reason,
					))
				}
			}
		case UpgradeInstallFiles:
			io.log("• 安装新版本文件")
			outcome.Files, stepErr = io.installFiles()
		case UpgradeRestartGuardian:
			io.log("• 重启保护服务(使新版本生效)")
			stepErr = io.restartGuardian()
		case UpgradeStartProtection:
			io.log("• 恢复保护")
			if stepErr = io.startProtection(); stepErr == nil {
				outcome.ProtectionRestored = true
			}
		}
		if stepErr != nil {
			// 欠条**留在盘上**:下一次 app-install 靠它知道还欠一次「起保护」。
			return outcome, errors.New(upgradeFailureMessageWithNetwork(step, stepErr, networkRestored))
		}
	}

	if err := io.clearIntent(); err != nil {
		outcome.IntentLeftBehind = true
		io.log(fmt.Sprintf("! 未能清除升级标记(%v):下次 app-install 会多做一次「恢复保护」,不影响本次结果", err))
	}
	return outcome, nil
}

func stepsContain(steps []UpgradeStep, want UpgradeStep) bool {
	for _, step := range steps {
		if step == want {
			return true
		}
	}
	return false
}
