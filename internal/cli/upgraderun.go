package cli

import (
	"errors"
	"fmt"
	"slices"

	"github.com/getbx/bx/internal/guardian"
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
// 抽出来是为了让**顺序本身**可测:「desired 必须在停保护之前读完」这条正确性
// 此前只由注释保证 —— 把它换成常量,整套测试照样全绿(复审实测)。现在它由
// upgraderun_test.go 里的调用序列钉住。
type upgradeIO struct {
	// guardianRunning 报告 Guardian 是否在跑。返回 error 表示**问不出来**,
	// 由 runUpgrade 按「在跑」处理(fail-closed),绝不压成 false。
	guardianRunning func() (bool, error)
	// loadDesiredOn 回答「升级前这台机器想不想开着保护」。
	//
	// 它**只问 Guardian 的 desired**:升级的停机改为武装维护挂起、不再改写
	// desired,所以一次中途失败之后重跑读到的就是用户本来的意图 —— 那正是
	// 升级欠条(upgrade-intent.json)能被删掉的全部依据。
	loadDesiredOn func() bool
	// confirm 询问用户。返回 (同意, 错误):false+nil 是「用户说不」,
	// 非 nil error 是「问不出来」—— 后者必须把整条命令带成非零退出,否则调用它的
	// 脚本会在 set -e 下继续往下走、把「什么都没做」打印成「完成」。
	confirm func(prompt string) (bool, error)
	// stopProtection 停保护,返回 bx down 那条路自己的结果。整个 result 一起
	// 带回来(而不是拆成 forced+cause 两个值)是有理由的:强制路径的**原因**不止
	// 两种,收尾文案要靠 forcedTeardownReason 区分,少带一个字段就会打印出假话。
	// 那条路是 best-effort,bx down 自己都拒绝断言「网络已还原」。
	stopProtection func() (macOSDownResult, error)
	// reassertDesiredOn 把「用户要保护」重新写回盘上。
	//
	// **只为过渡升级(新 CLI × 旧 Guardian)存在,而且只在那时跑。** 旧 Guardian
	// 的 Manager.Down 无条件 SaveDesired(DesiredOff)——`?reason=upgrade` 在那一版
	// 里只保住升级欠条,从不抑制那次写入;而欠条已经在同一条分支里被删掉。于是
	// 第一次升级停机之后,盘上是一句**自洽的**假话:desired=off、没有挂起、没有
	// 欠条。它不会自己暴露(desired=off 与「没有保护」互相印证,Diverge 一个字
	// 都不说),而一次崩溃就让重跑读到 off ——「恢复保护」那一步从计划里消失,
	// 升级还会报完成。
	//
	// 判据是**服务这次停机的那个 Guardian 声明了什么能力**,不是版本号:能力集
	// 随每一个回 Status 的响应发布(applyVersionFields),而停机的响应正是那份
	// 状态。声明了 maintenance_hold ⇒ 它没动过 desired ⇒ 这里一个字节都不写。
	reassertDesiredOn func() error
	installFiles      func() (installedFiles, error)
	restartGuardian   func() error
	startProtection   func() error
	log               func(string)
}

// upgradeOutcome 描述这次 app-install 实际做了什么,供调用方如实汇报 ——
// 收尾文案必须由**做过的事**导出,不能由「打算做什么」导出。
type upgradeOutcome struct {
	Files              installedFiles
	Cancelled          bool
	ProtectionRestored bool
	// Down 是停保护那一步的完整结果 —— 收尾文案必须能区分强制路径的每一种
	// 原因(见 forcedTeardownReason),所以这里不把它拆扁。
	Down           macOSDownResult
	ForcedTeardown bool
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

	// 意图必须在动手之前读完:退回路径(挂起写不成)上停机仍会写 desired=off,
	// 读晚了就只能读到那个 off。
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
			restoreIntentAfterHoldUnawareStop(io, desiredOn, down)
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
			// **desired 没有被这次升级改写过**(停机武装的是维护挂起),所以重跑
			// 读到的仍是用户本来的意图;那张挂起也会在 15 分钟后自己失效。
			return outcome, errors.New(upgradeFailureMessageWithNetwork(step, stepErr, networkRestored))
		}
	}

	return outcome, nil
}

// restoreIntentAfterHoldUnawareStop 修补一个**旧** Guardian 刚刚写下的 desired=off。
//
// 位置是刻意的:紧接着停机那一步,**早于装文件**,而且不看这一步成没成功。
// 崩在装文件或重启守护进程那一刻的机器,重跑时读到的就是这里写下的值;写在
// 编排末尾等于只覆盖「一切顺利」那条路,而那条路本来就不需要它。
//
// 到这里为止,所有会因为 desired 改变行为的东西都已经过去了:干净路径的
// Manager.Down 已经返回(旧 Guardian 的 m.current 也已清空,它不会再把 Core
// 拉回来),强制路径已经把 Guardian bootout 掉。之后 restartGuardian 拉起来的
// 是新 Guardian,而拦住它在换到一半的二进制上起 Core 的是**维护挂起**,不是
// 这句 desired ——(挂起由 recordStopIntent 在停机之前武装,与本函数无关)。
//
// **不产生 error。** 写不成时这一次升级仍会在末尾把保护起回来(desiredOn 为真,
// 计划里就有那一步),丢的只是「中途崩了还能重跑」这一层;把它升级成失败会在
// 保护已经停掉之后中止升级,那是更坏的一头。
func restoreIntentAfterHoldUnawareStop(io upgradeIO, desiredOn bool, down macOSDownResult) {
	if !desiredOn || statusDeclaresMaintenanceHold(down.Status) {
		return
	}
	if io.reassertDesiredOn == nil {
		io.log("! 无法把「用户要保护」写回磁盘(此平台不可用):若升级中途失败,重跑时保护可能不会自动恢复,届时执行 sudo bx up")
		return
	}
	if err := io.reassertDesiredOn(); err != nil {
		io.log(fmt.Sprintf(
			"! 未能把「用户要保护」写回磁盘(%v):这次升级仍会在末尾恢复保护,"+
				"但若中途失败,重跑不会自动恢复 —— 那时请执行 sudo bx up", err,
		))
	}
}

// statusDeclaresMaintenanceHold 报告服务这次停机的 Guardian 认不认识维护挂起。
//
// **缺席按「不认识」处理**,与菜单侧 declaresMaintenanceHold 同一条纪律:强制
// 拆除那条路返回的是 CLI 自己合成的 Status(没有能力集),而那时最需要把意图写
// 回去。往「多写一次 desired=on」偏是安全的(它本来就是用户的意图),往
// 「假定新版、什么都不做」偏则正好是 C1 那个永久无保护的状态。
func statusDeclaresMaintenanceHold(status guardian.Status) bool {
	return slices.Contains(status.Capabilities, guardian.CapabilityMaintenanceHold)
}

func stepsContain(steps []UpgradeStep, want UpgradeStep) bool {
	for _, step := range steps {
		if step == want {
			return true
		}
	}
	return false
}
