package cli

import (
	"errors"
	"strings"
	"testing"

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
	startErr        error

	reassertErr   error
	reassertCalls int

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
		reassertDesiredOn: func() error {
			f.calls = append(f.calls, "reassertDesiredOn")
			f.reassertCalls++
			return f.reassertErr
		},
		installFiles: func() (installedFiles, error) {
			f.calls = append(f.calls, "installFiles")
			return installedFiles{Version: "2.0.0", AppPath: "/Applications/Bx.app"}, f.installErr
		},
		restartGuardian: func() error {
			f.calls = append(f.calls, "restartGuardian")
			return f.restartErr
		},
		startProtection: func() error {
			f.calls = append(f.calls, "startProtection")
			return f.startErr
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
	fake := &fakeUpgradeIO{running: true, desiredOn: true, confirmAnswer: true}
	if _, err := runUpgrade(fake.io(), false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	load := indexOfCall(fake.calls, "loadDesiredOn")
	stop := indexOfCall(fake.calls, "stopProtection")
	if load < 0 || stop < 0 {
		t.Fatalf("calls = %v,必须读意图、停保护", fake.calls)
	}
	if load > stop {
		t.Fatalf("读意图(%d)必须早于停保护(%d),否则读到的永远是 off:%v", load, stop, fake.calls)
	}
}

// 中途失败不得声称保护已恢复 —— 收尾状态只能由**做过的事**导出。
func TestRunUpgradeReportsNoRestoreWhenAStepFails(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(*fakeUpgradeIO)
	}{
		{"装文件失败", func(f *fakeUpgradeIO) { f.installErr = errors.New("boom") }},
		{"重启 Guardian 失败", func(f *fakeUpgradeIO) { f.restartErr = errors.New("boom") }},
		{"起保护失败", func(f *fakeUpgradeIO) { f.startErr = errors.New("boom") }},
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
	for _, want := range []string{"stopProtection", "restartGuardian", "startProtection"} {
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
	for _, forbidden := range []string{"stopProtection", "installFiles", "restartGuardian", "startProtection"} {
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
	if indexOfCall(fake.calls, "stopProtection") < 0 {
		t.Fatalf("--yes 不改变要做的事:%v", fake.calls)
	}
}

// 走过强制拆除之后,后续步骤失败不得断言「网络仍可正常使用」——
// bx down 自己在同样处境下都拒绝这么说。
func TestRunUpgradeDoesNotClaimUsableNetworkAfterForcedTeardown(t *testing.T) {
	fake := &fakeUpgradeIO{
		running: true, desiredOn: true, confirmAnswer: true,
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
	if strings.Contains(err.Error(), "网络仍可正常使用") {
		t.Fatalf("走过强制拆除就不能断言网络可用,实际 = %q", err)
	}
	if !strings.Contains(err.Error(), "未经确认") {
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
	for _, forbidden := range []string{"stopProtection", "installFiles"} {
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
	f := &fakeUpgradeIO{running: true, desiredOn: true, confirmAnswer: true}
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
	if strings.Contains(err.Error(), "只有 sudo bx down") {
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
	f := &fakeUpgradeIO{running: true, desiredOn: true, confirmAnswer: true}
	f.stopStatus = guardian.Status{Protection: guardian.ProtectionOff}

	if _, err := runUpgrade(f.io(), false); err != nil {
		t.Fatalf("确认关闭之后升级应照常进行: %v", err)
	}
	if indexOfCall(f.calls, "installFiles") < 0 {
		t.Fatalf("应当换过文件, calls=%v", f.calls)
	}
}

// **「这台机器此刻要不要保护」必须由停机那一步知道。**
//
// 它决定要不要武装维护挂起:一台用户自己关着保护的机器上跑升级,武装出来的挂起
// 没有任何东西会去清它 —— upgradeSteps(true,false) 里根本没有「恢复保护」这一步,
// 而销挂起只发生在用户显式的 up/down/migrate 上。于是那 15 分钟里菜单表头写着
// Paused、bx status 写着「保护此刻被有意压制」,而机器关着只是因为用户想关着。
//
// 传的是**升级一开始就读好的那个值**,不是在停机里再读一次:退回路径会在停机
// 途中把 desired 写成 off,第二次读拿到的就不是用户的意图了。
func TestRunUpgradePassesTheProtectionIntentIntoTheStop(t *testing.T) {
	for _, desiredOn := range []bool{true, false} {
		f := &fakeUpgradeIO{running: true, desiredOn: desiredOn, confirmAnswer: true}
		f.stopStatus = holdAwareStopStatus()
		if _, err := runUpgrade(f.io(), false); err != nil {
			t.Fatalf("desiredOn=%v: %v", desiredOn, err)
		}
		if len(f.stopProtectionWanted) != 1 || f.stopProtectionWanted[0] != desiredOn {
			t.Fatalf("desiredOn=%v:停机那一步收到的是 %v", desiredOn, f.stopProtectionWanted)
		}
	}
}

// holdAwareStopStatus 是**本版** Guardian 服务这次停机之后回的那份状态:它声明了
// maintenance_hold,于是 desired 一个字节都没被动过。
func holdAwareStopStatus() guardian.Status {
	return guardian.Status{Protection: guardian.ProtectionOff, Capabilities: guardian.GuardianCapabilities()}
}

// **过渡升级(新 CLI × 旧 Guardian)必须把用户的意图写回盘上。**
//
// 旧 Guardian 的 Manager.Down 无条件 SaveDesired(DesiredOff)——`?reason=upgrade`
// 在那一版里只保住欠条,从不抑制那次写入。而欠条本身已经在这条分支里被删掉,
// 于是这次升级停机之后盘上是一句自洽的假话:desired=off,没有挂起,没有欠条。
// 后果不是「这一次升级失败」,是**永久无保护且悄无声息**:中途崩一次,重跑读到
// off、不再有「恢复保护」这一步,还会报「升级完成」;desired=off 自洽,
// Diverge 一个字都不说。
func TestRunUpgradeRestoresIntentWhenTheStopWasServedByAHoldUnawareGuardian(t *testing.T) {
	f := &fakeUpgradeIO{running: true, desiredOn: true, confirmAnswer: true}
	f.stopStatus = guardian.Status{Protection: guardian.ProtectionOff} // 旧 Guardian:不声明任何能力

	if _, err := runUpgrade(f.io(), false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.reassertCalls != 1 {
		t.Fatalf("旧 Guardian 服务的停机之后必须把 desired 写回 on,实际调用 %d 次:%v", f.reassertCalls, f.calls)
	}
	// **必须早于换二进制。** 崩在装文件那一步的机器,重跑时读到的就是这次写回的值;
	// 写在末尾等于只覆盖「一切顺利」那条路,而那条路本来就不需要它。
	reassert, install := indexOfCall(f.calls, "reassertDesiredOn"), indexOfCall(f.calls, "installFiles")
	if reassert < 0 || install < 0 || reassert > install {
		t.Fatalf("写回意图(%d)必须早于装文件(%d):%v", reassert, install, f.calls)
	}
}

// 本版 Guardian 服务的停机**不许**再写一次 desired:它从来就没被改过,
// 而多一个写入点就是多一个能撒谎的地方(设计取舍:desired 只由用户改)。
func TestRunUpgradeLeavesDesiredAloneWhenTheGuardianDeclaresMaintenanceHold(t *testing.T) {
	f := &fakeUpgradeIO{running: true, desiredOn: true, confirmAnswer: true}
	f.stopStatus = holdAwareStopStatus()

	if _, err := runUpgrade(f.io(), false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.reassertCalls != 0 {
		t.Fatalf("认识挂起的 Guardian 没动过 desired,不该再写一次:%v", f.calls)
	}
}

// 一台用户自己关着保护的机器,升级绝不许顺手把它打开 —— 这正是被删掉的那张
// 欠条当年的活 bug(陈旧欠条压过用户明确的关闭请求)。
func TestRunUpgradeNeverTurnsProtectionOnForAMachineThatWantsItOff(t *testing.T) {
	f := &fakeUpgradeIO{running: true, desiredOn: false, confirmAnswer: true}
	f.stopStatus = guardian.Status{Protection: guardian.ProtectionOff}

	if _, err := runUpgrade(f.io(), false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.reassertCalls != 0 {
		t.Fatalf("desired 本来就是 off,升级不得把它写成 on:%v", f.calls)
	}
}

// 停机那一步**自己失败**时更要写回:那正是「中途崩了」的样子,而强制拆除已经
// 把六步破坏性动作做完、旧 Guardian 也已经写下 off。
func TestRunUpgradeRestoresIntentEvenWhenTheStopItselfFailed(t *testing.T) {
	f := &fakeUpgradeIO{running: true, desiredOn: true, confirmAnswer: true}
	f.stopStatus = guardian.Status{Protection: guardian.ProtectionOff}
	f.stopErr = errors.New("强制拆除未能全部完成")

	if _, err := runUpgrade(f.io(), false); err == nil {
		t.Fatal("停机失败必须报错")
	}
	if f.reassertCalls != 1 {
		t.Fatalf("停机失败之后同样要写回意图,实际调用 %d 次:%v", f.reassertCalls, f.calls)
	}
}

// 写回失败不许静悄悄:那时这台机器就落在 C1 描述的那个状态里,而用户唯一的
// 线索只能是这一行。
func TestRunUpgradeWarnsWhenItCannotRestoreTheIntent(t *testing.T) {
	f := &fakeUpgradeIO{running: true, desiredOn: true, confirmAnswer: true}
	f.stopStatus = guardian.Status{Protection: guardian.ProtectionOff}
	f.reassertErr = errors.New("read-only file system")

	if _, err := runUpgrade(f.io(), false); err != nil {
		t.Fatalf("写回失败不该让这次升级整个失败(这一次仍会在末尾起保护): %v", err)
	}
	joined := strings.Join(f.logs, "\n")
	if !strings.Contains(joined, "read-only file system") || !strings.Contains(joined, "sudo bx up") {
		t.Fatalf("必须说清失败原因与用户的出路:\n%s", joined)
	}
}

// 「问不出来」不该把升级堵死。
//
// 扫描失败/不支持时用户做什么都清不掉这个状态:重跑升级会撞上同一道闸门,而菜单的
// Repair 走的正是 app-install —— 送修复的通道自己被堵住了。方向要与「停止不许依赖
// 别的先成功」一致:不确定不该变成寸步难行。
func TestRunUpgradeProceedsWithAWarningWhenTheScanItselfFailed(t *testing.T) {
	f := &fakeUpgradeIO{running: true, desiredOn: true, confirmAnswer: true}
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
