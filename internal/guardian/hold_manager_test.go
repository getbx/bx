package guardian

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"slices"
	"strings"
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
	if got := env.manager.Status().LastError; got != "desired_state_read_failed" {
		t.Fatalf("desired 那一半读不出来必须点名 desired,失败码 = %q", got)
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
	// 码必须点名**挂起**:报成 desired_state_read_failed 会把用户送去
	// guardian-state.json,而唯一的出路在 maintenance-hold.json。
	if got := env.manager.Status().LastError; got != "intent_unreadable" {
		t.Fatalf("挂起读不出来的失败码 = %q, want intent_unreadable", got)
	}
}

// 发布出去的码必须真的能换来一句可操作的话,而且点名的是**挂起那个文件**。
// 「失败必须留下可操作线索」——指引把人送去另一个文件比不给指引更糟。
func TestIntentUnreadableHintNamesTheHoldFile(t *testing.T) {
	msg := guardianHTTPError("/v1/up", http.StatusInternalServerError,
		[]byte(`{"error":"guardian operation failed","code":"intent_unreadable"}`)).Error()
	if !strings.Contains(msg, defaultMaintenanceHoldPath) {
		t.Fatalf("指引必须点名 %s,实际:%s", defaultMaintenanceHoldPath, msg)
	}
	if strings.Contains(msg, "guardian-state.json") {
		t.Fatalf("指引不许把用户送去 desired 那个文件,实际:%s", msg)
	}
}

// 挂起删不掉时 Up 会拒绝启动(见 TestUpRefusesToTurnProtectionOnWhenTheHoldCannotBeCleared),
// 而那个码同样必须换来一句可操作的话 —— 响应体刻意不外传原始错误串,用户看不到
// daemon 写的那句「read-only file system」。
func TestMaintenanceHoldClearFailedHintNamesTheHoldFile(t *testing.T) {
	msg := guardianHTTPError("/v1/up", http.StatusInternalServerError,
		[]byte(`{"error":"guardian operation failed","code":"`+maintenanceHoldClearFailedCode+`"}`)).Error()
	if !strings.Contains(msg, defaultMaintenanceHoldPath) {
		t.Fatalf("指引必须点名 %s,实际:%s", defaultMaintenanceHoldPath, msg)
	}
}

// **recovery_incomplete 是锁存的,而唯一的出路是 down 再 up。**
//
// Up 的第一句就撞上 recoveryBlocked,那句检查排在销挂起之前 —— 于是 `bx up`
// 既不清挂起也不启动,重跑多少次都一样。Down 会清掉它。没有这句指引,用户手里
// 只有一个自己解释不了自己的码。
func TestRecoveryIncompleteHintNamesTheEscape(t *testing.T) {
	msg := guardianHTTPError("/v1/up", http.StatusInternalServerError,
		[]byte(`{"error":"guardian operation failed","code":"recovery_incomplete"}`)).Error()
	if !strings.Contains(msg, "bx down") {
		t.Fatalf("指引必须说出 down 再 up 这条唯一的出路,实际:%s", msg)
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

// 挂起分支发 off 之前必须**问系统**,和隔壁 desired==off 那一支一样。
//
// 「账本说此刻不该有保护」证明不了「系统里没有 Core 在跑」—— legacy Core、
// 另一个终端里的 sudo bx run、记录丢失的机器,账本里都看不见。这一跳比 Down
// 那一跳更要紧:Guardian 每次重启都会跑它,于是它会把上一次求证出来的判断抹掉,
// 换成一句没求证过的 off。
//
// **今天升级停机走的是隔壁那支;Task 4 把它搬到这一支上来** —— 求证若不跟着搬,
// 就会恰好在它当初被写出来的那条路上悄悄消失。
func TestStartupRecoveryUnderArmedHoldDoesNotClaimOffWhileACoreIsStillRunning(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.store.SaveDesired(DesiredOn); err != nil {
		t.Fatal(err)
	}
	if err := env.store.ArmMaintenanceHold(HoldReasonUpgrade, time.Now()); err != nil {
		t.Fatal(err)
	}
	env.runner.scanResult = []Process{{PID: 4242}}

	if err := env.manager.Recover(context.Background()); err != nil {
		t.Fatalf("扫到 Core 不该让启动恢复失败: %v", err)
	}
	if got := env.manager.Status().Protection; got == ProtectionOff {
		t.Fatal("挂起期间也不许只凭账本就说 off —— 那会抹掉上一次求证过的判断")
	}
	if got := env.manager.Status().LastError; got != "core_still_running" {
		t.Fatalf("没能确认要说清是哪一种, LastError = %q", got)
	}
	// 「没能确认」绝不能顺带把关闭的路堵死(2026-08-04 的机制)。
	if env.manager.recoveryBlocked {
		t.Fatal("没能确认 + 挂起,仍不许堵死关闭的路")
	}
	if err := env.manager.Down(context.Background()); errors.Is(err, errRecoveryIncomplete) {
		t.Fatal("Down 必须仍然可达")
	}
}

// 求证失败(不是「确知还在」)同样不许说 off,也同样不许堵死 Down。
// 半边输入空间的断言等于没断言:上一条只覆盖 core_still_running。
func TestStartupRecoveryUnderArmedHoldKeepsShutdownReachableWhenScanFails(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.store.SaveDesired(DesiredOn); err != nil {
		t.Fatal(err)
	}
	if err := env.store.ArmMaintenanceHold(HoldReasonUpgrade, time.Now()); err != nil {
		t.Fatal(err)
	}
	env.runner.scanErr = errors.New("sysctl 挂了")

	if err := env.manager.Recover(context.Background()); err != nil {
		t.Fatalf("扫描失败不该让启动恢复失败: %v", err)
	}
	if got := env.manager.Status().LastError; got != "core_scan_failed" {
		t.Fatalf("「问不出来」必须与「确知还在」区分开, LastError = %q", got)
	}
	if env.manager.recoveryBlocked {
		t.Fatal("「没能确认」绝不能顺带把关闭的路堵死")
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
	if got := env.manager.Status().LastError; got != "intent_unreadable" {
		t.Fatalf("挂起读不出来的失败码 = %q, want intent_unreadable", got)
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
	env.manager.clearMaintenanceHold(holdClearedByDown)
	if _, armed, err := env.store.LoadMaintenanceHold(time.Now()); err != nil || armed {
		t.Fatalf("挂起必须被清掉:armed=%v err=%v", armed, err)
	}

	// 清不掉的情形(路径是个非空目录 ⇒ os.Remove 失败):不许 panic、不许把错误
	// 捅给调用方,**但必须留下痕迹**。只调一次「看它不炸」是个空断言 —— 实现即使
	// 根本不去碰 store 也照样绿。这里断言那行日志真的写出来了:「失败必须留下
	// 可操作线索」,而这条路唯一的线索就是 Guardian 日志。
	breakHoldRead(t, env)
	if err := os.WriteFile(env.store.paths.MaintenanceHold+"/occupied", []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	var logged bytes.Buffer
	restore := swapGuardianLogOutput(&logged)
	env.manager.clearMaintenanceHold(holdClearedByUp)
	restore()
	if !strings.Contains(logged.String(), "guardian_maintenance_hold_clear_failed") {
		t.Fatalf("清不掉挂起必须落日志,实际日志:%q", logged.String())
	}
	// 失败那一行也要说清是**哪条路**在清:排查时「用户点了 Turn On」与「升级
	// 自己那次停机」是完全不同的两个故事,而盘上看不出区别。
	if !strings.Contains(logged.String(), "by=up") {
		t.Fatalf("清不掉的那一行没说是谁要清的,实际日志:%q", logged.String())
	}
}

// **启动恢复里有一条合法地跑在挂起检查之前的路:recoverUpdateLocked。**
//
// 一台崩在升级中途的机器(update journal 停在 rolling_back)重启后,新 Guardian
// 起手就跑启动恢复,而 recoverUpdateLocked 在读意图快照**之前**就把快照里的
// 二进制回滚回去并起一个 Core —— 挂起武装着也照起不误。
//
// **这是有意不拦的,别把它「修」成一道栅栏:**
//   - 它起的不是半换的二进制:回滚先把快照还原回去,起来的是还原后的那一版;
//   - 拦住它会把一次做了一半的 Guardian 自更新永久卡在中间态,而那比它可能造成的
//     麻烦更糟 —— 与「停止/恢复路径不得依赖可能已失效的前置条件」同一个方向。
//
// 因此「挂起武装 ⇒ Guardian 一个 Core 都不会起」这句话是**假的**,本测试就是
// 把这条例外钉在明面上,免得后来的人照着那句话去补一道栅栏。
func TestStartupRecoveryStillFinishesAnInFlightUpdateUnderArmedHold(t *testing.T) {
	env := newUpdateTestEnv(t)
	manager := env.restartedManager(t, PhaseRollingBack, "")
	if err := env.store.SaveDesired(DesiredOn); err != nil {
		t.Fatal(err)
	}
	if err := env.store.ArmMaintenanceHold(HoldReasonUpgrade, time.Now()); err != nil {
		t.Fatal(err)
	}

	if err := manager.Recover(context.Background()); err != nil {
		t.Fatalf("挂起不该让崩在升级中途的机器无法完成回滚: %v; events=%#v", err, env.events.snapshot())
	}
	events := env.events.snapshot()
	if !containsEvent(events, "install.restore") {
		t.Fatalf("回滚必须照常做完(否则机器停在半换状态), events=%#v", events)
	}
	if !containsEvent(events, "core.start.v1") {
		t.Fatalf("回滚会起一个 Core,这条例外是有意的;若它真的消失了,说明有人给 "+
			"recoverUpdateLocked 加了栅栏 —— 先读本测试的注释再改。events=%#v", events)
	}
}

// 升级自己的那次停保护经 POST /v1/down?reason=upgrade 进来,**不许改写 desired**。
func TestMaintenanceDownLeavesDesiredOnAndKeepsHoldArmed(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.manager.Up(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := env.store.ArmMaintenanceHold(HoldReasonUpgrade, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := env.manager.Down(withMaintenanceStop(context.Background())); err != nil {
		t.Fatal(err)
	}
	desired, err := env.store.LoadDesired()
	if err != nil || desired != DesiredOn {
		t.Fatalf("升级的停机改写了 desired:%q err=%v", desired, err)
	}
	if _, armed, err := env.store.LoadMaintenanceHold(time.Now()); err != nil || !armed {
		t.Fatalf("升级自己的那次 Down 把挂起销了:armed=%v err=%v", armed, err)
	}
	// 拆除照常做完 —— 少写一句 desired 不等于少拆一样东西。
	env.assertBarrierRemoved(t)
}

// 用户显式的 down 清挂起,**且与 Down 的成败无关**。
func TestUserDownClearsHoldEvenWhenDownFails(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.manager.Up(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := env.store.ArmMaintenanceHold(HoldReasonUpgrade, time.Now()); err != nil {
		t.Fatal(err)
	}
	env.dns.restoreErr = errors.New("networksetup timed out") // 让 Down 走失败分支
	if err := env.manager.Down(context.Background()); err == nil {
		t.Fatal("DNS 还原失败仍应报错 —— 否则这条测试没走到它自称测的失败分支")
	}
	if _, armed, err := env.store.LoadMaintenanceHold(time.Now()); err != nil || armed {
		t.Fatalf("Down 失败就不销挂起:armed=%v err=%v", armed, err)
	}
}

// **Guardian 自发起 Core 的第四条路**:Down 在 DNS 还原失败时的补偿重启
// (manager.go:697-699)。它的原意是「还原失败了,别把用户停在一个奇怪的中间态」,
// 而在维护窗口里「把 Core 放回去」放回去的是一个**正换到一半的二进制**。
func TestMaintenanceDownWithFailedDNSRestoreDoesNotRestartCore(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.manager.Up(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := env.store.ArmMaintenanceHold(HoldReasonUpgrade, time.Now()); err != nil {
		t.Fatal(err)
	}
	startsBefore := env.runner.startCount()
	env.dns.restoreErr = errors.New("networksetup timed out")
	env.events.reset()

	var logged syncBuffer
	restore := swapGuardianLogOutput(&logged)
	err := env.manager.Down(withMaintenanceStop(context.Background()))
	restore()
	if err == nil {
		t.Fatal("DNS 还原失败仍应报错")
	}
	// **被压制的那条自发起 Core 的路必须留下一行。** 另外三条各有自己的一行,
	// 唯独这一条曾完全无声:真机上只看得到一次没有下文的 restore 失败,而
	// 「Core 为什么没像往常那样被放回去」无从解释。
	if !strings.Contains(logged.String(), "guardian_down_restore_recovery_held") {
		t.Fatalf("压制没留痕迹,实际日志:%q", logged.String())
	}
	if got := env.runner.startCount(); got != startsBefore {
		t.Fatalf("维护窗口里把 Core 放回去了:start = %d, want %d", got, startsBefore)
	}
	// **拆除照常做完。** 少做的只有「重启 Core」那一个动作;屏障是覆盖整个公网的
	// /2 reject 路由,留下就是整机断网,而拆它从不依赖 Core 在跑(正常的 Down
	// 成功路径就是这么做的)。
	if !slices.Contains(env.events.snapshot(), "barrier.remove") {
		t.Fatalf("屏障没拆 —— 停止不许因为少做一个动作就把用户留在断网里。events=%v", env.events.snapshot())
	}
	// 而这次 Down 依然没有改写 desired。
	if desired, err := env.store.LoadDesired(); err != nil || desired != DesiredOn {
		t.Fatalf("维护停机改写了 desired:%q err=%v", desired, err)
	}
	// 挂起也还在:它是这一整段窗口的凭据,不能被一次失败的 DNS 还原顺手销掉。
	if _, armed, err := env.store.LoadMaintenanceHold(time.Now()); err != nil || !armed {
		t.Fatalf("维护停机把挂起销了:armed=%v err=%v", armed, err)
	}
}

// 对照组:**没有**维护标记时这条补偿重启照旧发生 —— 半边输入空间的断言等于
// 没断言,而这一期不打算削弱那条既有补偿。
func TestUserDownWithFailedDNSRestoreStillRestartsCore(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.manager.Up(context.Background()); err != nil {
		t.Fatal(err)
	}
	startsBefore := env.runner.startCount()
	env.dns.restoreErr = errors.New("networksetup timed out")

	if err := env.manager.Down(context.Background()); err == nil {
		t.Fatal("DNS 还原失败仍应报错")
	}
	if got := env.runner.startCount(); got != startsBefore+1 {
		t.Fatalf("补偿重启被误伤了:start = %d, want %d", got, startsBefore+1)
	}
}

// 用户显式的 up 同样清挂起(设计取舍四:显式动作永远压过挂起)。
func TestUpClearsMaintenanceHold(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.store.ArmMaintenanceHold(HoldReasonUpgrade, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := env.manager.Up(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, armed, err := env.store.LoadMaintenanceHold(time.Now()); err != nil || armed {
		t.Fatalf("bx up 之后挂起还武装着:armed=%v err=%v", armed, err)
	}
}

// **清不掉挂起就绝不许把保护打开。**
//
// 「读得出来、却删不掉」(/var/lib/bx 不可写)此前只记一行日志:Up 照常走完、
// 报 Protected,而那张挂起还武装着 —— 此后 Core 一旦退出,handleUnexpectedExit
// 走 HoldArmed 那一支:**既不重启、也不装屏障**,于是保护在用户以为开着的时候
// 悄悄退回明文直连。那正是「Up 里销挂起」这件事本身要防的状态。
//
// 方向是安全的:这里拒绝的是**启动**,不是停止 —— 「停止永不依赖别的先成功」
// 那条不变量管的是 Down,而 Down 的销挂起仍然是 best-effort、只记日志。
func TestUpRefusesToTurnProtectionOnWhenTheHoldCannotBeCleared(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.store.ArmMaintenanceHold(HoldReasonUpgrade, time.Now()); err != nil {
		t.Fatal(err)
	}
	env.store.setClearError(errors.New("read-only file system"))

	if err := env.manager.Up(context.Background()); err == nil {
		t.Fatal("清不掉挂起时 Up 必须失败 —— 否则 bx up 会打印一个干净的 Protected")
	}
	if got := env.manager.Status().Protection; got == ProtectionProtected {
		t.Fatal("挂起还武装着,不许报 Protected")
	}
	if got := env.runner.startCount(); got != 0 {
		t.Fatalf("挂起还武装着就把 Core 起来了(start=%d):它一退出就再也不会被拉回来", got)
	}
	// 失败必须留下可操作线索:码经响应体到 CLI,再由 guardianCodeHints 翻成出路。
	if got := env.manager.Status().LastError; got != "maintenance_hold_clear_failed" {
		t.Fatalf("失败码 = %q, want maintenance_hold_clear_failed", got)
	}
}

// Migrate 是 `bx up` 在带 legacy Core 的机器上走的同一件事,同样不许放行。
func TestMigrateRefusesToTurnProtectionOnWhenTheHoldCannotBeCleared(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.store.ArmMaintenanceHold(HoldReasonUpgrade, time.Now()); err != nil {
		t.Fatal(err)
	}
	env.store.setClearError(errors.New("read-only file system"))

	err := env.manager.Migrate(context.Background(), MigrationRequest{
		Gateway: "192.0.2.1", ServerBypass: []string{"198.51.100.10/32"},
	})
	if err == nil {
		t.Fatal("清不掉挂起时 Migrate 必须失败")
	}
	if got := env.manager.Status().Protection; got == ProtectionProtected {
		t.Fatal("挂起还武装着,不许报 Protected")
	}
}

// 保护已经开着时 `bx up` 走的是**另一条**出口(upLocked 开头那句「已 Protected
// 就直接返回」),而它同样是用户显式说的 on。销挂起若挂在写 desired 那一段之后,
// 这条路就漏了:挂起继续武装着,而 Core 一旦退出,handleUnexpectedExit 会 fail-closed
// 不把它拉回来 —— 保护在用户以为开着的时候悄悄消失。
func TestUpClearsMaintenanceHoldWhenAlreadyProtected(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.manager.Up(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := env.manager.Status().Protection; got != ProtectionProtected {
		t.Fatalf("测试前提不成立:第一次 Up 之后应当已 Protected,实际 %q", got)
	}
	if err := env.store.ArmMaintenanceHold(HoldReasonUpgrade, time.Now()); err != nil {
		t.Fatal(err)
	}
	startsBefore := env.runner.startCount()
	if err := env.manager.Up(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := env.runner.startCount(); got != startsBefore {
		t.Fatalf("测试前提不成立:第二次 Up 应当走「已 Protected」那条早返回,实际起了 Core(%d→%d)", startsBefore, got)
	}
	if _, armed, err := env.store.LoadMaintenanceHold(time.Now()); err != nil || armed {
		t.Fatalf("已 Protected 的那条早返回漏了销挂起:armed=%v err=%v", armed, err)
	}
}

// Migrate 是 `bx up` 在一台还带 legacy Core 的机器上走的那条路,同样是用户显式
// 说的 on —— 漏了它,升级完的第一次开机保护会把挂起留在盘上。
func TestMigrateClearsMaintenanceHold(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.store.ArmMaintenanceHold(HoldReasonUpgrade, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := env.manager.Migrate(context.Background(), MigrationRequest{
		Gateway:      "192.0.2.1",
		ServerBypass: []string{"198.51.100.10/32"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, armed, err := env.store.LoadMaintenanceHold(time.Now()); err != nil || armed {
		t.Fatalf("Migrate 之后挂起还武装着:armed=%v err=%v", armed, err)
	}
}

// 启动恢复**绝不许**清挂起 —— 每一次升级都会重启 Guardian 跑一遍它,而那正是
// 挂起最该生效的时刻。
//
// **这条测试证明的是结果,不是机制。** 它对「销挂起被挪进 upLocked」是瞎的:
// 挂起武装时 recoverLocked 在 `intent.HoldArmed` 那一支就 return 了,压根走不到
// upLocked(实测:做那个变异本测试仍绿)。它拦得住的是把销挂起挪到 recoverLocked
// 里、或挪到 Recover 顶上这类改动 —— 也就是「启动恢复结束后挂起还在不在」这个
// 对外可见的性质本身。
//
// 前一条(TestStartupRecoveryUnderArmedHoldSkipsCoreAndKeepsDownReachable)钉住
// 武装态下 Core 不被起来;这里补上「文件仍在盘上」那半边。
func TestStartupRecoveryUnderArmedHoldDoesNotClearTheHold(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.store.SaveDesired(DesiredOn); err != nil {
		t.Fatal(err)
	}
	if err := env.store.ArmMaintenanceHold(HoldReasonUpgrade, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := env.manager.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, armed, err := env.store.LoadMaintenanceHold(time.Now()); err != nil || !armed {
		t.Fatalf("启动恢复把挂起销了 —— 升级窗口里它正靠这张挂起压住 Core:armed=%v err=%v", armed, err)
	}
}
