package guardian

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

// breakHoldRead 把挂起文件变成一个目录,于是 os.ReadFile 返回 EISDIR ——
// 一次**真实的**读失败,而不是替身里注入的一个假错误。
//
// 刻意不用 chmod 000:root 身份(CI 容器、以及 Guardian 自己跑的身份)照样读得动,
// 那会变成假绿(Task 1 已经踩过一次)。
func breakHoldRead(t *testing.T, env *managerTestEnv) {
	t.Helper()
	if err := os.MkdirAll(env.store.paths.MaintenanceHold, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := env.store.LoadMaintenanceHold(time.Now()); err == nil {
		t.Fatal("挂起本该读不出来,却读成功了 —— 后面的断言会变成假绿")
	}
}

// Core 退出**不是崩溃**,只要维护挂起还武装着:那是升级自己要求它停的。
// 此时既不许装屏障(升级正需要网络可用),也不许把它重启回来 ——
// forcedMacOSTeardown 与一个活着的 Guardian 赛跑,正是设计取舍二那张表。
func TestUnexpectedCoreExitUnderArmedHoldDoesNotRestartCore(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.manager.Up(context.Background()); err != nil {
		t.Fatal(err)
	}
	startsBefore := env.runner.startCount()
	if err := env.store.ArmMaintenanceHold(HoldReasonUpgrade, time.Now()); err != nil {
		t.Fatal(err)
	}
	env.events.reset()
	env.manager.handleUnexpectedExit(env.runner.currentProcess(), errors.New("Core exited"))

	if got := env.runner.startCount(); got != startsBefore {
		t.Fatalf("挂起期间 Core 被重启了:start = %d, want %d", got, startsBefore)
	}
	if events := env.events.snapshot(); containsEvent(events, "barrier.install") {
		t.Fatalf("挂起期间装了屏障:升级正需要网络可用。events=%v", events)
	}
}

// 过期的挂起不再压制任何东西 —— 半边输入空间的断言等于没断言。
func TestUnexpectedCoreExitWithExpiredHoldRestartsCore(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.manager.Up(context.Background()); err != nil {
		t.Fatal(err)
	}
	startsBefore := env.runner.startCount()
	if err := env.store.ArmMaintenanceHold(HoldReasonUpgrade, time.Now().Add(-2*MaintenanceHoldDuration)); err != nil {
		t.Fatal(err)
	}
	env.manager.handleUnexpectedExit(env.runner.currentProcess(), errors.New("Core crashed"))

	if got := env.runner.startCount(); got != startsBefore+1 {
		t.Fatalf("过期挂起仍在压制重启:start = %d, want %d", got, startsBefore+1)
	}
}

// 问不出来 ≠ 没有挂起。读盘失败一律 fail-closed:不重启 Core。
//
// 这一条走的是 **desired 那一半**读不出来(setLoadError 注入)。
func TestIntentSnapshotReadFailureDoesNotRestartCore(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.manager.Up(context.Background()); err != nil {
		t.Fatal(err)
	}
	startsBefore := env.runner.startCount()
	process := env.runner.currentProcess()
	env.store.setLoadError(errors.New("permission denied"))
	env.manager.handleUnexpectedExit(process, errors.New("Core exited"))

	if got := env.runner.startCount(); got != startsBefore {
		t.Fatalf("读不出意图时仍重启了 Core:start = %d, want %d", got, startsBefore)
	}
	if got := env.manager.Status().LastError; got == "" {
		t.Fatal("读不出意图必须留下失败码")
	}
}

// **挂起那一半**读不出来时同样不许重启 Core。
//
// 与上一条分开写不是重复:LoadIntentSnapshot 有两条错误分支,而
// setLoadError 只够得着第一条 —— 把挂起那条错误吞掉改成「没有挂起」,上一条
// 测试照样绿(本仓库上一期七条 review finding 里就有这个形状)。
func TestUnexpectedCoreExitWithUnreadableHoldDoesNotRestartCore(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.manager.Up(context.Background()); err != nil {
		t.Fatal(err)
	}
	startsBefore := env.runner.startCount()
	process := env.runner.currentProcess()
	breakHoldRead(t, env)
	env.manager.handleUnexpectedExit(process, errors.New("Core exited"))

	if got := env.runner.startCount(); got != startsBefore {
		t.Fatalf("挂起读不出来时仍重启了 Core:start = %d, want %d", got, startsBefore)
	}
	if got := env.manager.Status().LastError; got == "" {
		t.Fatal("读不出意图必须留下失败码")
	}
}

// 启动恢复也是「Guardian 自己发起 Core」的一条路,而且**每次升级都会走它**
// (restartGuardianForUpgrade 把 daemon 停掉再拉起来)。它同样必须认挂起。
//
// **并且绝不许把 Down 的路堵死。** recoverLocked 里 recoveryBlocked=true 会让
// Manager.Down 第一句就返回 errRecoveryIncomplete —— 2026-08-04 那次「用户 71
// 分钟关不掉保护」的机制。一次挂起不是恢复失败。
func TestStartupRecoveryUnderArmedHoldSkipsCoreAndKeepsDownReachable(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.store.SaveDesired(DesiredOn); err != nil {
		t.Fatal(err)
	}
	if err := env.store.ArmMaintenanceHold(HoldReasonUpgrade, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := env.manager.Recover(context.Background()); err != nil {
		t.Fatalf("挂起不是恢复失败: %v", err)
	}
	if got := env.runner.startCount(); got != 0 {
		t.Fatalf("挂起期间启动恢复起了 Core:start = %d", got)
	}
	// 盘上的 desired 一个字节都不该被动过:挂起与 desired 正交,这是整份设计
	// 的前提。恢复分支若顺手写一次 off,升级结束后用户的意图就丢了。
	if desired, err := env.store.Store.LoadDesired(); err != nil || desired != DesiredOn {
		t.Fatalf("desired 必须原样保持 on: %q err=%v", desired, err)
	}
	if err := env.manager.Down(context.Background()); errors.Is(err, errRecoveryIncomplete) {
		t.Fatal("挂起把关闭的路堵死了 —— 停止永不许依赖别的先成功")
	}
}

// 挂起读不出来:启动恢复必须 fail-closed(不起 Core),**但仍不许堵死 Down**。
//
// 这条是本任务在 brief 之外自己加的一道闸,理由是一条闭环:Task 1 让坏挂起
// (坏 schema / 坏 reason / 时钟跳变造出的远期过期)一律**报错**,并明确把
// 「任何一条显式 up/down 都能清掉它」(ClearMaintenanceHold 不看内容)当作唯一
// 的恢复路径。若读挂起失败也置 recoveryBlocked=true,Up 与 Down 双双永久短路,
// 那条恢复路径就被自己掐断了 —— 用一个新文件把 2026-08-04 的事故原样复活。
func TestStartupRecoveryWithUnreadableHoldFailsClosedButKeepsDownReachable(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.store.SaveDesired(DesiredOn); err != nil {
		t.Fatal(err)
	}
	breakHoldRead(t, env)

	if err := env.manager.Recover(context.Background()); err == nil {
		t.Fatal("挂起读不出来时启动恢复必须报错")
	}
	if got := env.runner.startCount(); got != 0 {
		t.Fatalf("读不出意图时启动恢复仍起了 Core:start = %d", got)
	}
	if env.manager.recoveryBlocked {
		t.Fatal("一张读不出来的挂起把 Down 永久堵死了 —— 而清掉它正是唯一的恢复路径")
	}
	if err := env.manager.Down(context.Background()); errors.Is(err, errRecoveryIncomplete) {
		t.Fatal("Down 必须仍然可达")
	}
}

// 反过来的那一半:**desired** 读不出来时,启动恢复照旧置 recoveryBlocked ——
// 这是既有行为(guardian-state.json 读不懂 = 连用户要什么都不知道),本任务
// 不得顺手放宽它。没有这条,上一条测试可以靠「一律不置位」平凡地绿。
func TestStartupRecoveryWithUnreadableDesiredStillBlocksRecovery(t *testing.T) {
	env := newManagerTestEnv(t)
	env.store.setLoadError(errors.New("permission denied"))

	if err := env.manager.Recover(context.Background()); err == nil {
		t.Fatal("desired 读不出来时启动恢复必须报错")
	}
	if !env.manager.recoveryBlocked {
		t.Fatal("desired 读不出来仍必须挡住后续 mutation(既有不变量)")
	}
	if got := env.manager.Status().LastError; got != "desired_state_read_failed" {
		t.Fatalf("失败码 = %q, want desired_state_read_failed", got)
	}
}

// LoadIntentSnapshot 的两条错误分支都必须真的报错。生产上跑的是 *Store 这一份
// (替身只覆盖 desired 那一半的注入),所以它得有自己的直接测试。
func TestStoreLoadIntentSnapshotReportsEitherHalfFailing(t *testing.T) {
	now := time.Now()

	t.Run("both readable", func(t *testing.T) {
		s := OpenStore(holdPaths(t.TempDir()))
		if err := s.SaveDesired(DesiredOn); err != nil {
			t.Fatal(err)
		}
		if err := s.ArmMaintenanceHold(HoldReasonUpgrade, now); err != nil {
			t.Fatal(err)
		}
		intent, err := s.LoadIntentSnapshot(now.Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if intent.Desired != DesiredOn || !intent.HoldArmed || intent.Hold.Reason != HoldReasonUpgrade {
			t.Fatalf("一次读出的意图不完整: %+v", intent)
		}
	})

	t.Run("desired unreadable", func(t *testing.T) {
		paths := holdPaths(t.TempDir())
		if err := os.MkdirAll(paths.Desired, 0o700); err != nil {
			t.Fatal(err)
		}
		s := OpenStore(paths)
		if err := s.ArmMaintenanceHold(HoldReasonUpgrade, now); err != nil {
			t.Fatal(err)
		}
		intent, err := s.LoadIntentSnapshot(now)
		if err == nil {
			t.Fatalf("desired 读不动必须报错: %+v", intent)
		}
		// desired 读不出来 = 连用户要什么都不知道,归到「挂起读不出来」那一类
		// 会让启动恢复不再置 recoveryBlocked,悄悄放宽既有不变量。
		if errors.Is(err, ErrMaintenanceHoldUnreadable) {
			t.Fatalf("desired 的失败被错分类成挂起的失败: %v", err)
		}
	})

	t.Run("hold unreadable", func(t *testing.T) {
		paths := holdPaths(t.TempDir())
		s := OpenStore(paths)
		if err := s.SaveDesired(DesiredOn); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(paths.MaintenanceHold, 0o700); err != nil {
			t.Fatal(err)
		}
		intent, err := s.LoadIntentSnapshot(now)
		if err == nil {
			t.Fatalf("挂起读不动必须报错,不许悄悄答「没有挂起」: %+v", intent)
		}
		if !errors.Is(err, ErrMaintenanceHoldUnreadable) {
			t.Fatalf("挂起那一半的失败必须可辨认(启动恢复据此不堵死 Down): %v", err)
		}
		if intent.HoldArmed {
			t.Fatalf("报错时不许同时说挂起武装着: %+v", intent)
		}
	})

	t.Run("no hold path at all", func(t *testing.T) {
		// 只填了一部分路径的替身(仓库里真有这种写法)必须当场炸掉,而不是
		// 得到一个「从来没有挂起」的空洞答案。
		s := OpenStore(Paths{Desired: t.TempDir() + "/guardian-state.json"})
		if _, err := s.LoadIntentSnapshot(now); err == nil {
			t.Fatal("没有挂起路径的 Store 必须拒绝作答")
		}
	})
}

// clearMaintenanceHold 是 best-effort:清得掉就清掉,清不掉只记日志。
// 它跑在 up/down 这两条路上,而「停止」永不许依赖先成功做成别的事。
func TestManagerClearMaintenanceHoldIsBestEffort(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.store.ArmMaintenanceHold(HoldReasonUpgrade, time.Now()); err != nil {
		t.Fatal(err)
	}
	env.manager.clearMaintenanceHold()
	if _, armed, err := env.store.LoadMaintenanceHold(time.Now()); err != nil || armed {
		t.Fatalf("挂起必须被清掉:armed=%v err=%v", armed, err)
	}

	// 清不掉的情形(路径是个非空目录 ⇒ os.Remove 失败)不许 panic、不许把
	// 错误捅给调用方 —— 它没有返回值,这一条钉的是「它不会变成一个返回错误
	// 的函数」这件行为。
	breakHoldRead(t, env)
	if err := os.WriteFile(env.store.paths.MaintenanceHold+"/occupied", []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	env.manager.clearMaintenanceHold()
}
