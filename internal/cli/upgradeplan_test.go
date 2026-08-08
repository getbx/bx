package cli

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// Guardian 没在跑就只装文件 —— 没有运行中的进程要换,也就没有断网的理由。
func TestUpgradeStepsWithoutRunningGuardianOnlyInstalls(t *testing.T) {
	got := upgradeSteps(false, false)
	want := []UpgradeStep{UpgradeInstallFiles}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("steps = %v, want %v", got, want)
	}
	if len(upgradeSteps(false, true)) != 1 {
		t.Fatal("Guardian 没在跑时,desired 是什么都不该多做")
	}
}

// 关键顺序:停保护必须排在装文件之前。
//
// 它让网络在整个换装期间是直连可用的,于是其后任何一步失败,最差只是「没有保护」,
// 而不是「路由指向已消失的 TUN、整机断网」—— 对一个翻墙工具,后者意味着用户连
// 重装包都下不了。
func TestUpgradeStepsStopProtectionBeforeInstalling(t *testing.T) {
	steps := upgradeSteps(true, true)
	stop, install := -1, -1
	for i, s := range steps {
		switch s {
		case UpgradeStopProtection:
			stop = i
		case UpgradeInstallFiles:
			install = i
		}
	}
	if stop < 0 || install < 0 {
		t.Fatalf("steps = %v, 必须同时包含停保护与装文件", steps)
	}
	if stop > install {
		t.Fatalf("停保护(%d)必须早于装文件(%d),否则失败会留下断网状态", stop, install)
	}
}

// 升级前开着,升级后要开回来;原本关着就不要擅自打开。
func TestUpgradeStepsRestoreDesiredState(t *testing.T) {
	on := upgradeSteps(true, true)
	if on[len(on)-1] != UpgradeStartProtection {
		t.Fatalf("原本开着,最后一步必须是起保护,实际 %v", on)
	}
	for _, s := range upgradeSteps(true, false) {
		if s == UpgradeStartProtection {
			t.Fatal("原本关着,不得擅自打开保护")
		}
	}
}

// Guardian 必须被重启,否则换了符号链接也没用 —— 已在跑的进程不会因此换代码。
// 这正是 2026-08-08 那次真机事故的根因。
func TestUpgradeStepsAlwaysRestartGuardianWhenItIsRunning(t *testing.T) {
	for _, desired := range []bool{true, false} {
		found := false
		for _, s := range upgradeSteps(true, desired) {
			if s == UpgradeRestartGuardian {
				found = true
			}
		}
		if !found {
			t.Fatalf("desired=%v:Guardian 在跑就必须重启它", desired)
		}
	}
}

// 确认文案必须明说会断网,而不是含糊的「可能有短暂中断」。
func TestUpgradeConfirmMessageStatesTheOutage(t *testing.T) {
	on := upgradeConfirmMessage(true)
	for _, must := range []string{"断网", "重启保护"} {
		if !strings.Contains(on, must) {
			t.Fatalf("确认文案必须包含 %q,实际 = %q", must, on)
		}
	}

	// desiredOn=false 时 Guardian 停/重启的是空转(没有 Core、没有 TUN、没有
	// 劫持路由可言),不该断言一个不会发生的断网 —— Guardian plist 是
	// KeepAlive=true,"Guardian 在跑、保护未开启" 正是任何一次 bx down 之后的
	// 常态,不是边角情况。
	off := upgradeConfirmMessage(false)
	if strings.Contains(off, "断网") {
		t.Fatalf("保护未开启时不该声称会断网,实际 = %q", off)
	}
}

// 失败文案必须说清「现在处于什么状态」,而不只是抛出错误。
func TestUpgradeFailureMessageSaysNetworkIsUsable(t *testing.T) {
	msg := upgradeFailureMessage(UpgradeStartProtection, errors.New("boom"))
	if !strings.Contains(msg, "网络") {
		t.Fatalf("装文件之后的失败必须说明网络仍可用(直连),实际 = %q", msg)
	}
	if !strings.Contains(msg, "uninstall") {
		t.Fatalf("失败必须给出真正管用的下一步(卸载重装),实际 = %q", msg)
	}
}

// 那句建议用户照做也没用:bx down 的干净路径不 bootout Guardian,
// 所以 down/up 只会让同一个旧 Guardian 重起一个旧 Core(2026-08-08 真机实证)。
// 照做也没用的指引比没有指引更糟 —— 它让用户以为自己已经处理过了。
func TestAppInstallDoesNotAdviseTheIneffectiveDownUp(t *testing.T) {
	source, err := os.ReadFile("appinstall_darwin.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	if strings.Contains(text, "bx down && sudo bx up") {
		t.Fatal("这句建议无效,不得再出现:app-install 必须自己把升级做完")
	}
	if !strings.Contains(text, "upgradeSteps(") {
		t.Fatal("app-install 必须按 upgradeSteps 执行完整升级")
	}
}
