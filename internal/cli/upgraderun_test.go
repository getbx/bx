package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/elevate"

	"github.com/getbx/bx/internal/guardian"
)

// fakeUpgradeIO 记录调用顺序 —— 本编排的正确性大半是**顺序**,不是返回值。
type fakeUpgradeIO struct {
	calls []string

	running    bool
	runningErr error
	desiredOn  bool

	confirmAnswer  bool
	confirmErr     error
	confirmPrompts []string

	stopForced      bool
	stopCause       error
	stopStatus      guardian.Status
	stopHoldFalback error
	stopErr         error
	installErr      error
	restartErr      error

	configUsable bool
	enableErr    error

	barrierErr, stopBehindErr, startBehindErr, handOverErr error

	stopProtectionWanted []bool

	logs []string
}

func (f *fakeUpgradeIO) io() upgradeIO {
	return upgradeIO{
		guardianRunning: func() (bool, error) {
			f.calls = append(f.calls, "guardianRunning")
			return f.running, f.runningErr
		},
		loadDesiredOn: func() bool {
			f.calls = append(f.calls, "loadDesiredOn")
			return f.desiredOn
		},
		confirm: func(prompt string) (bool, error) {
			f.calls = append(f.calls, "confirm")
			f.confirmPrompts = append(f.confirmPrompts, prompt)
			return f.confirmAnswer, f.confirmErr
		},
		stopProtection: func(protectionWanted bool) (macOSDownResult, error) {
			f.calls = append(f.calls, "stopProtection")
			f.stopProtectionWanted = append(f.stopProtectionWanted, protectionWanted)
			return macOSDownResult{
				Forced:       f.stopForced,
				Cause:        f.stopCause,
				Status:       f.stopStatus,
				HoldFallback: f.stopHoldFalback,
			}, f.stopErr
		},
		installFiles: func() (installedFiles, error) {
			f.calls = append(f.calls, "installFiles")
			return installedFiles{Version: "2.0.0", AppPath: "/Applications/Bx.app"}, f.installErr
		},
		restartGuardian: func() error {
			f.calls = append(f.calls, "restartGuardian")
			return f.restartErr
		},
		configUsable: func() bool { return f.configUsable },
		enableGuardian: func() error {
			f.calls = append(f.calls, "enableGuardian")
			return f.enableErr
		},
		barrierUp: func() error {
			f.calls = append(f.calls, "barrierUp")
			return f.barrierErr
		},
		stopGuardianBehindBarrier: func() error {
			f.calls = append(f.calls, "stopGuardianBehindBarrier")
			return f.stopBehindErr
		},
		startGuardianBehindBarrier: func() error {
			f.calls = append(f.calls, "startGuardianBehindBarrier")
			return f.startBehindErr
		},
		handOver: func() error {
			f.calls = append(f.calls, "handOver")
			return f.handOverErr
		},
		log: func(line string) { f.logs = append(f.logs, line) },
	}
}

func indexOfCall(calls []string, want string) int {
	for i, call := range calls {
		if call == want {
			return i
		}
	}
	return -1
}

// 意图必须在停保护之前读完。
//
// **退回路径**(挂起写不成时退回写 desired=off,设计取舍三)上停保护仍会把
// Guardian 的 desired 写成 off,之后再读就永远是 off —— 读晚了,保护就再也回不来。
// 这条此前只由一句注释保证。
func TestRunUpgradeReadsIntentBeforeStoppingProtection(t *testing.T) {
	// 意图决定走哪条路(保护开着 ⇒ 屏障下切换),所以两条路都必须先读它。
	for _, tc := range []struct {
		desiredOn bool
		firstMove string
	}{{false, "stopProtection"}, {true, "barrierUp"}} {
		fake := &fakeUpgradeIO{running: true, desiredOn: tc.desiredOn, confirmAnswer: true}
		if _, err := runUpgrade(fake.io(), false); err != nil {
			t.Fatalf("desiredOn=%v: unexpected error: %v", tc.desiredOn, err)
		}
		load := indexOfCall(fake.calls, "loadDesiredOn")
		move := indexOfCall(fake.calls, tc.firstMove)
		if load < 0 || move < 0 {
			t.Fatalf("desiredOn=%v: calls = %v,必须读意图、再 %s", tc.desiredOn, fake.calls, tc.firstMove)
		}
		if load > move {
			t.Fatalf("desiredOn=%v: 读意图(%d)必须早于 %s(%d):%v", tc.desiredOn, load, tc.firstMove, move, fake.calls)
		}
	}
}

// 中途失败不得声称保护已恢复 —— 收尾状态只能由**做过的事**导出。
func TestRunUpgradeReportsNoRestoreWhenAStepFails(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(*fakeUpgradeIO)
	}{
		{"装文件失败", func(f *fakeUpgradeIO) { f.installErr = errors.New("boom") }},
		{"停旧 Guardian 失败", func(f *fakeUpgradeIO) { f.stopBehindErr = errors.New("boom") }},
		{"起新 Guardian 失败", func(f *fakeUpgradeIO) { f.startBehindErr = errors.New("boom") }},
		{"交接失败", func(f *fakeUpgradeIO) { f.handOverErr = errors.New("boom") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeUpgradeIO{running: true, desiredOn: true, confirmAnswer: true}
			tc.set(fake)
			outcome, err := runUpgrade(fake.io(), false)
			if err == nil {
				t.Fatal("这一步失败必须报错")
			}
			if outcome.ProtectionRestored {
				t.Fatal("没起成保护就不能说恢复了")
			}
		})
	}
}

// 问不出「Guardian 在不在跑」必须当成「在跑」。
//
// 压成 false 会让计划退化成「只装文件」——文件在活着的旧 Guardian 底下被换掉,
// 正是本期要消灭的那个 bug,而且末尾还会报「升级完成」。
func TestRunUpgradeTreatsUnknownGuardianStateAsRunning(t *testing.T) {
	fake := &fakeUpgradeIO{runningErr: errors.New("launchctl 说不清"), desiredOn: true, confirmAnswer: true}
	outcome, err := runUpgrade(fake.io(), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, want := range []string{"barrierUp", "stopGuardianBehindBarrier", "installFiles", "startGuardianBehindBarrier", "handOver"} {
		if indexOfCall(fake.calls, want) < 0 {
			t.Fatalf("问不出来就得按「在跑」走完整流程,缺 %s:%v", want, fake.calls)
		}
	}
	if !outcome.ProtectionRestored {
		t.Fatal("完整流程跑完应报告保护已恢复")
	}
}

// 收尾状态必须由**做过的事**导出。Guardian 没在跑时什么都没启动,不能说
// 「保护已恢复」。
func TestRunUpgradeDoesNotClaimProtectionItNeverStarted(t *testing.T) {
	fake := &fakeUpgradeIO{running: false, desiredOn: true}
	outcome, err := runUpgrade(fake.io(), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if outcome.ProtectionRestored {
		t.Fatalf("一步保护都没起过,不得声称已恢复:%v", fake.calls)
	}
	if indexOfCall(fake.calls, "confirm") >= 0 {
		t.Fatalf("没有保护要停就不该打扰用户:%v", fake.calls)
	}
}

// 取消就是一步都不做。
func TestRunUpgradeCancelledDoesNothing(t *testing.T) {
	fake := &fakeUpgradeIO{running: true, desiredOn: true, confirmAnswer: false}
	outcome, err := runUpgrade(fake.io(), false)
	if err != nil || !outcome.Cancelled {
		t.Fatalf("取消不该报错,outcome=%+v err=%v", outcome, err)
	}
	for _, forbidden := range []string{"stopProtection", "barrierUp", "stopGuardianBehindBarrier", "installFiles", "restartGuardian", "handOver"} {
		if indexOfCall(fake.calls, forbidden) >= 0 {
			t.Fatalf("取消后不得执行 %s:%v", forbidden, fake.calls)
		}
	}
}

// --yes 跳过提问,但不跳过任何一步。
func TestRunUpgradeAssumeYesSkipsOnlyTheQuestion(t *testing.T) {
	fake := &fakeUpgradeIO{running: true, desiredOn: true}
	if _, err := runUpgrade(fake.io(), true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if indexOfCall(fake.calls, "confirm") >= 0 {
		t.Fatalf("--yes 不该再问:%v", fake.calls)
	}
	if indexOfCall(fake.calls, "barrierUp") < 0 || indexOfCall(fake.calls, "handOver") < 0 {
		t.Fatalf("--yes 不改变要做的事:%v", fake.calls)
	}
}

// 走过强制拆除之后,后续步骤失败不得断言「网络仍可正常使用」——
// bx down 自己在同样处境下都拒绝这么说。
func TestRunUpgradeDoesNotClaimUsableNetworkAfterForcedTeardown(t *testing.T) {
	fake := &fakeUpgradeIO{
		running: true, desiredOn: false, confirmAnswer: true,
		stopForced: true, stopCause: errors.New("guardian 无响应"),
		installErr: errors.New("boom"),
	}
	outcome, err := runUpgrade(fake.io(), false)
	if err == nil {
		t.Fatal("装文件失败必须报错")
	}
	// 原因现在住在 outcome.Down 里(整个 result 一起回传,收尾文案才能区分强制
	// 路径的每一种原因);断言的东西没变:原因必须到得了调用方。
	if !outcome.ForcedTeardown || outcome.Down.Cause == nil {
		t.Fatalf("强制拆除必须被上报给调用方,outcome=%+v", outcome)
	}
	if strings.Contains(err.Error(), "The network still works") {
		t.Fatalf("走过强制拆除就不能断言网络可用,实际 = %q", err)
	}
	if !strings.Contains(err.Error(), "has not been confirmed") {
		t.Fatalf("必须说明网络状态未经确认,实际 = %q", err)
	}
}

// 「问不出来」必须是非零退出,不能和「用户说不」一样静静地成功返回。
//
// 生成的 install.sh 在 set -euo pipefail 下紧接着无条件打印「完成」。若这里
// return nil,一次非交互 SSH 升级就会:什么都没做 → 退出 0 → install.sh 说
// 「完成」→ 旧 daemon 继续跑旧代码,而用户以为升级落地了。那正是本期要消灭的
// 形状,只不过换了一扇门进来。
func TestRunUpgradeFailsLoudlyWhenItCannotAsk(t *testing.T) {
	fake := &fakeUpgradeIO{running: true, desiredOn: true, confirmErr: errCannotAsk}
	outcome, err := runUpgrade(fake.io(), false)
	if err == nil {
		t.Fatal("问不出来必须报错(非零退出),否则上层脚本会把它当成升级成功")
	}
	if outcome.Cancelled {
		t.Fatal("「问不出来」不是「用户取消」,不得混为一谈")
	}
	if !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("必须告诉调用方怎么显式表态,实际 = %q", err)
	}
	for _, forbidden := range []string{"stopProtection", "barrierUp", "installFiles"} {
		if indexOfCall(fake.calls, forbidden) >= 0 {
			t.Fatalf("没问成就不得动手 %s:%v", forbidden, fake.calls)
		}
	}
}

// 用户明确说「不」仍然是一次正常结束(退出码 0),不该报错。
func TestRunUpgradeExplicitNoIsNotAnError(t *testing.T) {
	fake := &fakeUpgradeIO{running: true, desiredOn: true, confirmAnswer: false}
	outcome, err := runUpgrade(fake.io(), false)
	if err != nil {
		t.Fatalf("用户说不是正常结束,不该报错:%v", err)
	}
	if !outcome.Cancelled {
		t.Fatal("必须如实标记为取消")
	}
}

// Guardian 没能确认保护关掉时,升级必须**停在第一步**,不许去换二进制。
//
// 这条以前漏了:判断只看 down.Forced 与 err,而「200 但 protection_state != off」
// 两者都不满足 —— 于是升级会照常换掉 runtime 二进制、重启 Guardian,而一个不受管的
// Core 还跑着老二进制、占着 TUN。症状要到之后才现:startProtection 撞上 latch 住的
// core_ownership_uncertain,而那个状态只有 down+up 能解 —— 远比现在中止更难懂。
func TestRunUpgradeStopsBeforeTouchingFilesWhenTheStopWasUnconfirmed(t *testing.T) {
	f := &fakeUpgradeIO{running: true, desiredOn: false, confirmAnswer: true}
	f.stopStatus = guardian.Status{
		Protection: guardian.ProtectionNeedsAttention,
		LastError:  "core_still_running",
	}

	_, err := runUpgrade(f.io(), false)
	if err == nil {
		t.Fatal("没能确认关闭时必须中止升级")
	}
	if !strings.Contains(err.Error(), "core_still_running") {
		t.Errorf("要说清是什么原因:%v", err)
	}
	if !strings.Contains(err.Error(), "bx-guard.err.log") {
		t.Errorf("要指向真能看到是哪个进程的地方:%v", err)
	}
	// 中止文案原来附了一句「(core_ownership_uncertain,只有 sudo bx down 再
	// sudo bx up 能解)」。那句现在是假的:用户发起的 up/migrate 每次都会重新
	// 向系统求证,而只要那个 Core 还在跑,down+up 一样会被拒。把用户支去做一件
	// 不管用的事,比不说更坏 —— 他会以为自己已经处理过了。
	if strings.Contains(err.Error(), "只有 "+elevate.Prefix+"bx down") {
		t.Errorf("中止文案还在声称 down+up 是唯一出路(而那个 Core 还在跑时它同样会被拒):%v", err)
	}
	// **文件一个字节都不许动。** 中止之所以安全,正因为它发生在第一步。
	if i := indexOfCall(f.calls, "installFiles"); i >= 0 {
		t.Fatalf("不许在保护状态未确认时换二进制, calls=%v", f.calls)
	}
	if i := indexOfCall(f.calls, "restartGuardian"); i >= 0 {
		t.Fatalf("更不许重启 Guardian, calls=%v", f.calls)
	}
}

// 回归:Guardian 确认关掉了,升级照常走完。
func TestRunUpgradeProceedsWhenTheStopWasConfirmed(t *testing.T) {
	f := &fakeUpgradeIO{running: true, desiredOn: false, confirmAnswer: true}
	f.stopStatus = guardian.Status{Protection: guardian.ProtectionOff}

	if _, err := runUpgrade(f.io(), false); err != nil {
		t.Fatalf("确认关闭之后升级应照常进行: %v", err)
	}
	if indexOfCall(f.calls, "installFiles") < 0 {
		t.Fatalf("应当换过文件, calls=%v", f.calls)
	}
}

// **保护开着的机器,升级绝不先停保护。** 停保护 = 停 Core、DNS 还给系统、不装屏障,
// 从那一刻到新 Core 劫持完成,流量无保护地直连 —— 泄漏真实 IP。所有者的边界是
// 「断网可以,泄漏 IP 不行」(2026-09-25),所以那条路上只能是「先装屏障」。
func TestRunUpgradeNeverStopsProtectionOnAProtectedMachine(t *testing.T) {
	f := &fakeUpgradeIO{running: true, desiredOn: true, confirmAnswer: true}
	outcome, err := runUpgrade(f.io(), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if indexOfCall(f.calls, "stopProtection") >= 0 || indexOfCall(f.calls, "restartGuardian") >= 0 {
		t.Fatalf("保护开着时走了会泄漏的「先停保护」那条路:%v", f.calls)
	}
	want := []string{"barrierUp", "stopGuardianBehindBarrier", "installFiles", "startGuardianBehindBarrier", "handOver"}
	last := -1
	for _, step := range want {
		i := indexOfCall(f.calls, step)
		if i < 0 || i < last {
			t.Fatalf("屏障下切换的顺序应为 %v,实际 %v", want, f.calls)
		}
		last = i
	}
	if !outcome.ProtectionRestored {
		t.Fatal("交接成功就是保护恢复了")
	}
}

// 屏障装上之后任何一步失败:断网,但没有东西漏出去 —— 话要这么说,而且绝不能说
// 「网络还能用(直连)」,更不能自己把屏障拆了。
func TestRunUpgradeFailureBehindTheBarrierSaysBlockedNotDirect(t *testing.T) {
	for _, set := range []func(*fakeUpgradeIO){
		func(f *fakeUpgradeIO) { f.stopBehindErr = errors.New("boom") },
		func(f *fakeUpgradeIO) { f.installErr = errors.New("boom") },
		func(f *fakeUpgradeIO) { f.startBehindErr = errors.New("boom") },
		func(f *fakeUpgradeIO) { f.handOverErr = errors.New("boom") },
	} {
		f := &fakeUpgradeIO{running: true, desiredOn: true, confirmAnswer: true}
		set(f)
		_, err := runUpgrade(f.io(), false)
		if err == nil {
			t.Fatal("这一步失败必须报错")
		}
		msg := err.Error()
		if !strings.Contains(msg, "blocking all traffic") || !strings.Contains(msg, "nothing is leaving unprotected") {
			t.Fatalf("屏障下的失败要说「在拦、没漏」:%q", msg)
		}
		if strings.Contains(msg, "The network still works") {
			t.Fatalf("屏障在却说网络能用:%q", msg)
		}
		if !strings.Contains(msg, elevate.Prefix+"bx down") {
			t.Fatalf("要给出用户自己选择不要保护的那条出路:%q", msg)
		}
	}
}

// 屏障没装上:什么都没改过,后面一步都不许做。
func TestRunUpgradeBarrierFailureTouchesNothingElse(t *testing.T) {
	f := &fakeUpgradeIO{running: true, desiredOn: true, confirmAnswer: true, barrierErr: errors.New("no gateway")}
	if _, err := runUpgrade(f.io(), false); err == nil {
		t.Fatal("屏障装不上必须报错")
	}
	for _, forbidden := range []string{"stopGuardianBehindBarrier", "installFiles", "startGuardianBehindBarrier", "handOver", "stopProtection"} {
		if indexOfCall(f.calls, forbidden) >= 0 {
			t.Fatalf("屏障没装上就不许往下走(%s):%v", forbidden, f.calls)
		}
	}
}

// 一台用户自己关着保护的机器,升级绝不许顺手把它打开 —— 这正是被删掉的那张
// 欠条当年的活 bug(陈旧欠条压过用户明确的关闭请求)。
func TestRunUpgradeNeverTurnsProtectionOnForAMachineThatWantsItOff(t *testing.T) {
	f := &fakeUpgradeIO{running: true, desiredOn: false, confirmAnswer: true}
	f.stopStatus = guardian.Status{Protection: guardian.ProtectionOff}

	outcome, err := runUpgrade(f.io(), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if indexOfCall(f.calls, "handOver") >= 0 || outcome.ProtectionRestored {
		t.Fatalf("desired 本来就是 off,升级不得把保护打开:%v", f.calls)
	}
}

// 「问不出来」不该把升级堵死。
//
// 扫描失败/不支持时用户做什么都清不掉这个状态:重跑升级会撞上同一道闸门,而菜单的
// Repair 走的正是 app-install —— 送修复的通道自己被堵住了。方向要与「停止不许依赖
// 别的先成功」一致:不确定不该变成寸步难行。
func TestRunUpgradeProceedsWithAWarningWhenTheScanItselfFailed(t *testing.T) {
	f := &fakeUpgradeIO{running: true, desiredOn: false, confirmAnswer: true}
	f.stopStatus = guardian.Status{
		Protection: guardian.ProtectionNeedsAttention,
		LastError:  "core_scan_failed",
	}

	if _, err := runUpgrade(f.io(), false); err != nil {
		t.Fatalf("扫不动不该把升级整个堵死: %v", err)
	}
	if indexOfCall(f.calls, "installFiles") < 0 {
		t.Fatalf("应当继续换文件, calls=%v", f.calls)
	}
	joined := strings.Join(f.logs, "\n")
	if !strings.Contains(joined, "core_scan_failed") {
		t.Errorf("但必须留下警告说明:\n%s", joined)
	}
}

// 全新安装必须**真的调到**那一步,而不只是把它排进计划里。
//
// 纯函数 upgradeSteps 已经有测试钉住计划长什么样,但计划正确而编排漏调,
// 症状与今天真机上遇到的一模一样:装完之后 guardian.sock 不存在,菜单栏一个
// 免密开关都点不动,而且三处都不留痕。
func TestRunUpgradeOnFreshInstallStartsGuardian(t *testing.T) {
	fake := &fakeUpgradeIO{running: false, desiredOn: true, configUsable: true}

	if _, err := runUpgrade(fake.io(), true); err != nil {
		t.Fatalf("runUpgrade = %v, want nil", err)
	}

	install := indexOfCall(fake.calls, "installFiles")
	enable := indexOfCall(fake.calls, "enableGuardian")
	if install < 0 || enable < 0 {
		t.Fatalf("全新安装应当装文件并拉起 Guardian, calls = %v", fake.calls)
	}
	if enable < install {
		t.Fatalf("Guardian 要在文件装好之后才拉起来(否则起的是旧二进制), calls = %v", fake.calls)
	}
	// 全新安装绝不开保护 —— 那是用户的决定。
	if indexOfCall(fake.calls, "startProtection") >= 0 {
		t.Fatalf("全新安装不许开启保护, calls = %v", fake.calls)
	}
	if indexOfCall(fake.calls, "stopProtection") >= 0 {
		t.Fatalf("没有运行中的 Guardian,不该停任何东西, calls = %v", fake.calls)
	}
}

// 配置还读不出来时绝不拉 Guardian:daemon 起不来,而 plist 带 KeepAlive=true,
// bootstrap 一个起不来的服务等于让机器每秒重启它一次,同时安装报告说完成。
func TestRunUpgradeOnFreshInstallSkipsGuardianWithoutUsableConfig(t *testing.T) {
	fake := &fakeUpgradeIO{running: false, desiredOn: true, configUsable: false}

	if _, err := runUpgrade(fake.io(), true); err != nil {
		t.Fatalf("runUpgrade = %v, want nil", err)
	}
	if indexOfCall(fake.calls, "enableGuardian") >= 0 {
		t.Fatalf("配置不可用时不许 bootstrap Guardian, calls = %v", fake.calls)
	}
	if indexOfCall(fake.calls, "installFiles") < 0 {
		t.Fatalf("文件还是要装的, calls = %v", fake.calls)
	}
}

// 拉不起来要如实报错,而且**不许套用「升级未完成」那套话** —— 这台机器上
// 没有任何东西被停过,网络一直是直连。
func TestRunUpgradeReportsGuardianStartFailureWithoutClaimingAnUpgradeStopped(t *testing.T) {
	fake := &fakeUpgradeIO{running: false, desiredOn: true, configUsable: true, enableErr: errors.New("bootstrap 失败")}

	_, err := runUpgrade(fake.io(), true)
	if err == nil {
		t.Fatal("拉不起 Guardian 必须报错 —— 静默等于把今天这个 bug 原样留着")
	}
	if !strings.Contains(err.Error(), ""+elevate.Prefix+"bx up") {
		t.Errorf("必须给出可操作的下一步, got %q", err.Error())
	}
	if strings.Contains(err.Error(), "升级未完成") || strings.Contains(err.Error(), "uninstall") {
		t.Errorf("全新安装失败不该说成升级中断、更不该建议卸载重装, got %q", err.Error())
	}
}
