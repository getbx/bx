package cli

import (
	"errors"
	"fmt"

	"github.com/getbx/bx/internal/elevate"
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
	// stopProtection 停保护。参数是「这台机器此刻要不要保护」——**升级一开始
	// 就读好的那个值**,由它决定这次停机要不要武装维护挂起。
	//
	// 不在停机里自己再读一次:退回路径(挂起写不成)会在停机途中把 desired 写成
	// off,第二次读拿到的就不是用户的意图了。
	stopProtection  func(protectionWanted bool) (macOSDownResult, error)
	installFiles    func() (installedFiles, error)
	restartGuardian func() error
	// configUsable 回答「Guardian 现在起得来吗」。
	//
	// 判据刻意与 daemon 启动时那一跳同源:它要读 /etc/bx/config.yaml 取 owner_uid,
	// 读不出或解析不了就直接退出 —— 而 plist 带 KeepAlive=true,于是 bootstrap 一个
	// 读不到配置的 Guardian 等于制造一个崩溃循环。**「还没跑过 bx setup」是全新安装
	// 的常态**,所以这不是边角情况。
	configUsable func() bool
	// enableGuardian 在全新安装之后把 Guardian 服务拉起来(bootstrap + kickstart)。
	// 它**不开保护** —— 起来时 desired 还是 off,只是开始服务控制 socket,
	// 而菜单栏的免密开关全靠那个 socket。幂等:服务已加载且可达时不发任何
	// launchctl 命令。
	enableGuardian func() error
	// 保护开着时的四步(D3),见 UpgradeBarrierUp 等常量的注释。barrierUp 失败时必须
	// 已经把自己做过的撤回(挂起、半装的屏障),于是「什么都没改」那句话成立。
	barrierUp                  func() error
	stopGuardianBehindBarrier  func() error
	startGuardianBehindBarrier func() error
	handOver                   func() error
	log                        func(string)
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
		io.log(fmt.Sprintf("! could not determine whether Guardian is running (%v): treating it as running, so protection will be stopped and the service restarted", err))
	}

	// 意图必须在动手之前读完:退回路径(挂起写不成)上停机仍会写 desired=off,
	// 读晚了就只能读到那个 off。
	desiredOn := io.loadDesiredOn()
	steps := upgradeSteps(running, desiredOn, io.guardianConfigUsable())
	// 两条路都会断网(屏障下切换是「拦住」,停保护是「停服务」),都要先问。
	stopsProtection := stepsContain(steps, UpgradeStopProtection) || stepsContain(steps, UpgradeBarrierUp)

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

	if stepsContain(steps, UpgradeBarrierUp) && (io.barrierUp == nil || io.stopGuardianBehindBarrier == nil || io.startGuardianBehindBarrier == nil || io.handOver == nil) {
		// 在屏障下切换的四步缺一步就不开始:退回旧的「先停保护」那条路就是泄漏。
		return outcome, errors.New("upgrading while protection is on needs the switch-behind-a-barrier steps, which are not available here; nothing was changed")
	}
	networkRestored := true
	barrierUp := false
	for _, step := range steps {
		var stepErr error
		switch step {
		case UpgradeStopProtection:
			io.log("• Stopping protection (the network goes back to direct for now)")
			down, err := io.stopProtection(desiredOn)
			outcome.Down = down
			outcome.ForcedTeardown = down.Forced || err != nil
			if outcome.ForcedTeardown {
				networkRestored = false
			}
			// **退回规则触发时必须让用户看到。** 它不产生 error(保护干净地停了、
			// 升级会照常走完),所以不专门报一行就彻底无声 —— 而后果实打实:盘上
			// 留下的是「用户不想要保护」,一台正在升级的机器于是与一台用户关掉了
			// 保护的机器再次长得一模一样。**这里是它唯一的生产渲染点**:
			// downReportLines 只被 `bx down`(downPurposeUser)调用,而那条路
			// 从不退回。
			if down.HoldFallback != nil {
				io.log("! " + holdFallbackWarning(down.HoldFallback))
			}
			stepErr = err
			// **Guardian 没能确认保护关掉时,不许继续换二进制。**
			//
			// 这条以前漏了:判断只看 down.Forced 与 err,而「200 但
			// protection_state != off」两者都不满足 —— 于是升级会照常换掉 runtime
			// 二进制、重启 Guardian,而一个不受管的 Core 还在跑着老二进制、占着 TUN。
			// 症状要到之后才现:startProtection 撞上 core_ownership_uncertain,
			// 而那个状态只要那个不受管的 Core 还占着 TUN 就一直解不开(用户发起的
			// up 每次都会重新求证,但求证的答案就是「还有一个在跑」)——
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
						"Guardian confirmed that a bx Core process is still running on this system (%s): swapping the binary now would leave an "+
							"unmanaged old Core holding the TUN, and the symptom only shows up later (core_ownership_uncertain — "+
							"every "+elevate.Prefix+"bx up re-verifies, but as long as that Core is running it keeps refusing). "+
							"Start with sudo tail -50 /var/log/bx-guard.err.log to find out which process it is, deal with it, then re-run the upgrade",
						reason,
					)
				} else {
					io.log(fmt.Sprintf(
						"! Guardian could not confirm that protection is off (%s): the upgrade continues, but if protection will not start afterwards, "+
							"start with sudo tail -50 /var/log/bx-guard.err.log", reason,
					))
				}
			}
		case UpgradeInstallFiles:
			io.log("• Installing the new version's files")
			outcome.Files, stepErr = io.installFiles()
		case UpgradeRestartGuardian:
			io.log("• Restarting the protection service (so the new version takes effect)")
			stepErr = io.restartGuardian()
		case UpgradeEnableGuardian:
			io.log("• Starting the protection service (protection itself stays off)")
			stepErr = io.enableGuardian()
		case UpgradeBarrierUp:
			io.log("• Blocking all traffic while protection restarts (nothing leaves unprotected)")
			if stepErr = io.barrierUp(); stepErr == nil {
				barrierUp = true
			}
		case UpgradeStopGuardianBehindBarrier:
			io.log("• Stopping the old protection service")
			stepErr = io.stopGuardianBehindBarrier()
		case UpgradeStartGuardianBehindBarrier:
			io.log("• Starting the new protection service")
			stepErr = io.startGuardianBehindBarrier()
		case UpgradeHandOver:
			io.log("• Handing over to the new version and restoring protection")
			if stepErr = io.handOver(); stepErr == nil {
				outcome.ProtectionRestored = true
			}
		}
		if stepErr != nil && barrierUp {
			return outcome, errors.New(upgradeFailureBehindBarrier(stepErr))
		}
		if stepErr != nil {
			// **desired 没有被这次升级改写过**(停机武装的是维护挂起),所以重跑
			// 读到的仍是用户本来的意图;那张挂起也会在 15 分钟后自己失效。
			return outcome, errors.New(upgradeFailureMessageWithNetwork(step, stepErr, networkRestored))
		}
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

// guardianConfigUsable 把「没人告诉我」读成「不可用」。
//
// **判错的代价不对称**,与生产接线处那条注释同源:答「能用」而其实不能,会
// bootstrap 一个起不来的 Guardian,配上 KeepAlive=true 就是崩溃循环;答「不能用」
// 而其实能,只是菜单栏要等用户敲一次 sudo bx up。所以缺省(测试替身没设这个钩子)
// 走保守那边 —— 而不是 panic:绝大多数升级测试与这一步无关,让它们全部被迫表态
// 只会制造噪声,真正需要它的测试自己会把钩子设上。
func (io upgradeIO) guardianConfigUsable() bool {
	return io.configUsable != nil && io.configUsable()
}
