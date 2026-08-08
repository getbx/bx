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
	// stopProtection 停保护。forced 为真表示走了 bx down 的强制拆除逃生路径 ——
	// 那条路是 best-effort,bx down 自己都拒绝断言「网络已还原」。
	stopProtection  func() (forced bool, cause error, err error)
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
	ForcedTeardown     bool
	ForcedCause        error
	IntentLeftBehind   bool
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
		if err != nil {
			// 问不出来 ≠ 用户说不。必须报错退出:静静地 return nil 会让
			// install.sh(set -e)紧接着打印「完成」,而旧 daemon 还跑着旧代码。
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
			forced, cause, err := io.stopProtection()
			outcome.ForcedTeardown, outcome.ForcedCause = forced, cause
			if forced {
				networkRestored = false
			}
			stepErr = err
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
