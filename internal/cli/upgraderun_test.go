package cli

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// fakeUpgradeIO 记录调用顺序 —— 本编排的正确性大半是**顺序**,不是返回值。
type fakeUpgradeIO struct {
	calls []string

	running    bool
	runningErr error
	desiredOn  bool

	saveIntentErr  error
	clearIntentErr error
	savedIntent    *bool
	intentCleared  bool

	confirmAnswer  bool
	confirmPrompts []string

	stopForced bool
	stopCause  error
	stopErr    error
	installErr error
	restartErr error
	startErr   error

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
		saveIntent: func(desiredOn bool) error {
			f.calls = append(f.calls, "saveIntent")
			f.savedIntent = &desiredOn
			return f.saveIntentErr
		},
		clearIntent: func() error {
			f.calls = append(f.calls, "clearIntent")
			f.intentCleared = f.clearIntentErr == nil
			return f.clearIntentErr
		},
		confirm: func(prompt string) bool {
			f.calls = append(f.calls, "confirm")
			f.confirmPrompts = append(f.confirmPrompts, prompt)
			return f.confirmAnswer
		},
		stopProtection: func() (bool, error, error) {
			f.calls = append(f.calls, "stopProtection")
			return f.stopForced, f.stopCause, f.stopErr
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

// 意图必须在停保护之前既读完又落盘。
//
// 停保护会把 Guardian 的 desired 写成 off,之后再读就永远是 off —— 读晚了,
// 保护就再也回不来。这条此前只由一句注释保证。
func TestRunUpgradeReadsAndRecordsIntentBeforeStoppingProtection(t *testing.T) {
	fake := &fakeUpgradeIO{running: true, desiredOn: true, confirmAnswer: true}
	if _, err := runUpgrade(fake.io(), false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	load := indexOfCall(fake.calls, "loadDesiredOn")
	save := indexOfCall(fake.calls, "saveIntent")
	stop := indexOfCall(fake.calls, "stopProtection")
	if load < 0 || save < 0 || stop < 0 {
		t.Fatalf("calls = %v,必须读意图、记欠条、停保护", fake.calls)
	}
	if load > stop {
		t.Fatalf("读意图(%d)必须早于停保护(%d),否则读到的永远是 off:%v", load, stop, fake.calls)
	}
	if save > stop {
		t.Fatalf("欠条(%d)必须在停保护(%d)之前落盘,否则中途失败没人知道该起回保护:%v", save, stop, fake.calls)
	}
	if fake.savedIntent == nil || !*fake.savedIntent {
		t.Fatalf("欠条必须记下 desiredOn=true,实际 %v", fake.savedIntent)
	}
}

// 中途失败必须把欠条留在盘上 —— 那正是重试唯一的信息来源。
func TestRunUpgradeKeepsIntentWhenAStepFails(t *testing.T) {
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
			if indexOfCall(fake.calls, "clearIntent") >= 0 {
				t.Fatalf("失败时不得清欠条,否则重试会以为不欠保护:%v", fake.calls)
			}
			if outcome.ProtectionRestored {
				t.Fatal("没起成保护就不能说恢复了")
			}
		})
	}
}

// 记不下欠条就不要开始 —— 此刻确实一步都没做,那句「状态未变」是真的。
func TestRunUpgradeRefusesToStartWhenIntentCannotBeRecorded(t *testing.T) {
	fake := &fakeUpgradeIO{running: true, desiredOn: true, confirmAnswer: true, saveIntentErr: errors.New("disk full")}
	_, err := runUpgrade(fake.io(), false)
	if err == nil {
		t.Fatal("记不下欠条必须拒绝开始")
	}
	if indexOfCall(fake.calls, "stopProtection") >= 0 {
		t.Fatalf("拒绝之后不得动手:%v", fake.calls)
	}
	if !strings.Contains(err.Error(), "状态未变") {
		t.Fatalf("此时状态确实未变,应如实说明,实际 = %q", err)
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
	if !fake.intentCleared {
		t.Fatal("干净跑完一次应清掉欠条")
	}
}

// 取消就是一步都不做。
func TestRunUpgradeCancelledDoesNothing(t *testing.T) {
	fake := &fakeUpgradeIO{running: true, desiredOn: true, confirmAnswer: false}
	outcome, err := runUpgrade(fake.io(), false)
	if err != nil || !outcome.Cancelled {
		t.Fatalf("取消不该报错,outcome=%+v err=%v", outcome, err)
	}
	for _, forbidden := range []string{"saveIntent", "stopProtection", "installFiles", "restartGuardian", "startProtection", "clearIntent"} {
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
	if !outcome.ForcedTeardown || outcome.ForcedCause == nil {
		t.Fatalf("强制拆除必须被上报给调用方,outcome=%+v", outcome)
	}
	if strings.Contains(err.Error(), "网络仍可正常使用") {
		t.Fatalf("走过强制拆除就不能断言网络可用,实际 = %q", err)
	}
	if !strings.Contains(err.Error(), "未经确认") {
		t.Fatalf("必须说明网络状态未经确认,实际 = %q", err)
	}
}

// 欠条落盘/读取/清除的往返,以及「一次失败的升级之后,重试仍知道欠一次保护」。
func TestUpgradeIntentRoundTripSurvivesAFailedUpgrade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "upgrade-intent.json")
	if _, present := loadUpgradeIntent(path); present {
		t.Fatal("没有文件就不该有欠条")
	}
	if err := saveUpgradeIntent(path, true); err != nil {
		t.Fatal(err)
	}
	pending, present := loadUpgradeIntent(path)
	if !present || !pending {
		t.Fatalf("欠条应为 (true,true),实际 (%v,%v)", pending, present)
	}
	// 失败的那次升级已经把 Guardian 的 desired 写成了 off:只看 store 会得出
	// 「不欠保护」,机器从此再不被保护。
	if !resolveUpgradeDesiredOn(false, pending, present) {
		t.Fatal("欠条说 on,重试就必须把保护起回来")
	}
	if err := clearUpgradeIntent(path); err != nil {
		t.Fatal(err)
	}
	if _, present := loadUpgradeIntent(path); present {
		t.Fatal("清过之后不该还有欠条")
	}
	if err := clearUpgradeIntent(path); err != nil {
		t.Fatalf("清一个不存在的欠条不算失败:%v", err)
	}
	if resolveUpgradeDesiredOn(false, false, false) {
		t.Fatal("没有欠条、store 也说 off,就不该擅自开保护")
	}
	if !resolveUpgradeDesiredOn(true, false, false) {
		t.Fatal("store 说 on 就是 on")
	}
}
