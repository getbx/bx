package guardian

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// 形状一:PID==0(扫描派生的锁存、孤儿 launching 标记)且盘上 desired=off。
// Down 走 manager.go:784 的提前返回,根本到不了那句 m.current = Process{}。
// 这条路今天 Down 报成功、锁存原样留着,而 CLI/菜单/升级文案四处都在告诉用户
// 「down 再 up 就能清」。
func TestDownClearsTheOwnershipLatchOnTheEarlyReturnPath(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.store.SaveDesired(DesiredOff); err != nil {
		t.Fatal(err)
	}
	env.manager.current = Process{Uncertain: true}

	if err := env.manager.Down(context.Background()); err != nil {
		t.Fatalf("Down = %v, want nil(这条形状本来就该干净返回)", err)
	}
	if env.manager.current.Uncertain {
		t.Fatal("提前返回那条路没有清掉锁存 —— 被四处文案引用的那条出路在最常见的形状下是假的")
	}
}

// 形状二(最常见):PID==0 且 desired=on —— 第一次失败的 Up 已经把 desired
// 写成 on。Down 落到 runner.Existing,而那正是当初把它锁上的同一次扫描:
// 持久条件还在就返回同一个错,Down 以 core_state_read_failed 失败。
// **失败也必须清掉锁存** —— 清除对拆除的成败无条件。
func TestDownClearsTheOwnershipLatchEvenWhenTeardownFails(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.store.SaveDesired(DesiredOn); err != nil {
		t.Fatal(err)
	}
	env.manager.current = Process{Uncertain: true}
	env.runner.existingErr = uncertainOwnership(
		Process{Uncertain: true},
		errors.New("no Core process record on disk, and scanning for running Cores failed: sysctl"),
	)

	if err := env.manager.Down(context.Background()); err == nil {
		t.Fatal("测试前提不成立:这条形状应当在 runner.Existing 上失败,否则测不到「失败路径也要清」")
	}
	if env.manager.current.Uncertain {
		t.Fatal("拆除失败时没有清掉锁存 —— 而用户此刻最需要的正是能重新开始")
	}
}

// 形状三:有真实 PID(handleUnexpectedExit 内联那一处)。
//
// **注意**:设计文档说这条形状必然停在 runner.Stop 开头的 verifyInstalledProcess
// (「对一个从没验明身份的进程必然失败」),那句诊断是错的 —— 这个 process 来自
// m.current,是一个验明过身份、带 Executable/UID/Generation 的真进程,
// verifyInstalledProcess 照过。真正会失败的是 Stop 里那次 removeRecordIfGeneration
// (与当初把锁存造出来的是同一次删记录),而它**只在持久条件还在时**失败。
// 所以这条测试不依赖是哪一跳失败:直接注入 Stop 的错,前提不成立就大声 t.Fatal。
func TestDownClearsTheOwnershipLatchCarryingARealPID(t *testing.T) {
	env := newProtectedManagerTestEnv(t)
	process := env.runner.currentProcess()
	env.runner.exit(process.PID, uncertainOwnership(process, errors.New("clear owned Core record after exit failed")))
	eventually(t, func() bool { return env.manager.Status().LastError == "core_ownership_uncertain" })
	if !env.manager.current.Uncertain {
		t.Fatal("测试前提不成立:这次退出没有留下锁存")
	}
	env.runner.stopErr = errors.New("verify recorded Core PID before shutdown: identity never verified")

	if err := env.manager.Down(context.Background()); err == nil {
		t.Fatal("测试前提不成立:注入了 Stop 失败,Down 应当报错")
	}
	if env.manager.current.Uncertain {
		t.Fatal("带真实 PID 的锁存没有被清掉")
	}
}

// 第四条路:`recoveryBlocked` 为真,Down 第一句就返回 errRecoveryIncomplete。
//
// 它盯住的是**注册位置**:defer 排在那道检查之前才跑得到。锁存与
// recoveryBlocked 恰恰最容易同时出现(Task 2 之前,一次开机时的瞬时扫描失败
// 会先锁住所有权、再由 recoverLocked 把 recoveryBlocked 一并落下),
// 而那时用户手里就只剩这一条命令了。
func TestDownClearsTheOwnershipLatchEvenWhenRecoveryIsBlocked(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.store.SaveDesired(DesiredOn); err != nil {
		t.Fatal(err)
	}
	env.manager.current = Process{Uncertain: true}
	env.manager.recoveryBlocked = true

	if err := env.manager.Down(context.Background()); !errors.Is(err, errRecoveryIncomplete) {
		t.Fatalf("测试前提不成立:Down = %v, want errRecoveryIncomplete", err)
	}
	if env.manager.current.Uncertain {
		t.Fatal("recoveryBlocked 那条早退路径没有清掉锁存 —— 清除必须无条件,包括这一条")
	}
}

// 启动恢复撞上所有权不确定时,失败照常上报、Core 照常不起 —— 但绝不许把
// 关闭的路一起堵死。recoveryBlocked 是 Down 的第一句判断,它为真时 Down 直接
// 返回 errRecoveryIncomplete:那正是 2026-08-04「用户 71 分钟关不掉保护」的机制。
func TestStartupRecoveryDoesNotBlockShutdownOnUncertainOwnership(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.store.SaveDesired(DesiredOn); err != nil {
		t.Fatal(err)
	}
	env.manager.current = Process{Uncertain: true}

	if err := env.manager.Recover(context.Background()); !errors.Is(err, ErrProcessOwnershipUncertain) {
		t.Fatalf("Recover = %v, want 所有权不确定(测试前提)", err)
	}
	if env.manager.recoveryBlocked {
		t.Fatal("所有权不确定把 recoveryBlocked 置真了 —— 「开不了」升级成了「关不掉」")
	}
	if err := env.manager.Down(context.Background()); errors.Is(err, errRecoveryIncomplete) {
		t.Fatal("Down 被 recoveryBlocked 挡住 —— 这正是 71 分钟事故的形状")
	}
}

// 反向:**别的**失败仍然要置 recoveryBlocked。这条改动只针对所有权不确定
// 这一种,放宽到全部就是把一道既有的栅栏顺手拆了。
func TestStartupRecoveryStillBlocksOnOtherUpFailures(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.store.SaveDesired(DesiredOn); err != nil {
		t.Fatal(err)
	}
	env.runner.existingErr = errors.New("inspect recorded Core PID 42: permission denied")

	if err := env.manager.Recover(context.Background()); err == nil {
		t.Fatal("Recover 应当失败(测试前提)")
	}
	if !env.manager.recoveryBlocked {
		t.Fatal("非所有权类的启动恢复失败不再置 recoveryBlocked —— 这条既有栅栏被顺手拆了")
	}
}

// **反向守卫之一:Down 不许清掉它自己刚造出来的那个锁存。**
//
// Down 在 DNS 还原失败时会把 Core 放回去(startCoreLocked)。那一步可能刚刚
// 落下一个**新的**锁存 —— 一个无条件的 defer 会把它一起抹掉,而那是 fail-open:
// 系统里可能真有一个没验明身份的 Core 在跑,下一次 Up 却畅通无阻。
//
// 这一条走的是「进门时**没有**锁存」那一格。
func TestDownDoesNotClearALatchItsOwnRestartJustCreated(t *testing.T) {
	env := newProtectedManagerTestEnv(t)
	env.dns.restoreErr = errors.New("resolver restore failed")
	env.runner.startErr = uncertainOwnership(
		Process{PID: 4242, Uncertain: true},
		errors.New("no Core process record on disk, but Core appears to be running (PID 4242)"),
	)

	if err := env.manager.Down(context.Background()); err == nil {
		t.Fatal("测试前提不成立:DNS 还原失败 + 重启失败时 Down 应当报错")
	}
	if !env.manager.current.Uncertain {
		t.Fatal("Down 把它自己在还原补偿里新造的锁存也抹掉了 —— 那是 fail-open")
	}
}

// **反向守卫之二:进门时就有锁存,而 Down 自己的还原补偿又造了一个新的。**
//
// 上一条守的是「进门时没有锁存」那个入口,它在身份比对被去掉之后**仍然绿**
// (第一道 !latchedOnEntry.Uncertain 早退挡住了)。真正盯住身份比对的是这一条:
// 进门时那个锁存(PID 101)与还原补偿新造的那个(PID 4242)是两个不同的值,
// 去掉 m.current != latchedOnEntry 之后 Down 会把 4242 那个一起抹掉 —— 而那正是
// 「系统里可能真有一个没验明身份的 Core 在跑,下一次 Up 却畅通无阻」。
func TestDownClearsOnlyTheEntryLatchWhenItsRestartCreatesAnother(t *testing.T) {
	env := newProtectedManagerTestEnv(t)
	process := env.runner.currentProcess()
	env.runner.exit(process.PID, uncertainOwnership(process, errors.New("clear owned Core record after exit failed")))
	eventually(t, func() bool { return env.manager.Status().LastError == "core_ownership_uncertain" })
	entryLatch := env.manager.current
	if !entryLatch.Uncertain || entryLatch.PID == 0 {
		t.Fatalf("测试前提不成立:进门时应当有一个带真实 PID 的锁存, got %+v", entryLatch)
	}
	env.dns.restoreErr = errors.New("resolver restore failed")
	env.runner.startErr = uncertainOwnership(
		Process{PID: 4242, Uncertain: true},
		errors.New("no Core process record on disk, but Core appears to be running (PID 4242)"),
	)

	if err := env.manager.Down(context.Background()); err == nil {
		t.Fatal("测试前提不成立:DNS 还原失败 + 重启失败时 Down 应当报错")
	}
	if !env.manager.current.Uncertain || env.manager.current.PID != 4242 {
		t.Fatalf("Down 抹掉了还原补偿新造的那个锁存(而不是只清进门时那一个)—— 那是 fail-open, got %+v", env.manager.current)
	}
}

// 锁存之后的错误必须**不比**第一次少信息。今天 upLocked/Migrate 短路时传的是
// nil cause,于是第二次 bx up 只剩一句「Core process ownership is uncertain」:
// 没有 PID、没有逃生提示、也看不出是十五个产地里的哪一个。
func TestLatchedRefusalKeepsTheOriginalCause(t *testing.T) {
	env := newManagerTestEnv(t)
	env.runner.startErr = uncertainOwnership(
		Process{PID: 4242, Uncertain: true},
		errors.New("no Core process record on disk, but Core appears to be running (PID 4242)"),
	)

	first := env.manager.Up(context.Background())
	if !errors.Is(first, ErrProcessOwnershipUncertain) {
		t.Fatalf("首次 Up = %v, want 所有权不确定(测试前提)", first)
	}
	if !strings.Contains(first.Error(), "PID 4242") {
		t.Fatalf("测试前提不成立:首次拒绝本来就该带上原因, got %q", first.Error())
	}

	second := env.manager.Up(context.Background())
	if !errors.Is(second, ErrProcessOwnershipUncertain) {
		t.Fatalf("再次 Up = %v, want 所有权不确定", second)
	}
	if !strings.Contains(second.Error(), "PID 4242") {
		t.Fatalf("锁存之后的错误比第一次更少信息 —— 用户第二次看到的反而是一句空话: %q", second.Error())
	}
}

// 迁移入口(bx up 在还带 legacy Core 的机器上走的那条)是同一条规矩。
//
// **注入一次「还有 Core 在跑」的扫描是必需的,不是布景。** Migrate 现在与 upLocked
// 一样先重新求证(见 TestMigrateReVerifiesOwnershipWhenTheSystemIsClean),而
// newManagerTestEnv 的零值扫描结果是一台干净机器 —— 不注入的话它会**释放**锁存、
// 干净返回,这条测试就再也造不出「锁存后的拒绝」这个前提了。它守的属性没变
// (拒绝必须带上当初那个 cause),变的只是造出该属性的前提。
// (第一次就扫脏 ⇒ 立即拒绝、不沉降,所以这条不需要缩 coreScanSettle。)
func TestLatchedMigrateRefusalKeepsTheOriginalCause(t *testing.T) {
	env := newManagerTestEnv(t)
	env.runner.scanResult = []Process{{PID: 4242}}
	env.manager.current = Process{PID: 4242, Uncertain: true}
	env.manager.uncertainCause = errors.New("spawned Core record has a live process whose identity was never verified")

	err := env.manager.Migrate(context.Background(), MigrationRequest{Gateway: "192.0.2.1", ServerBypass: []string{"198.51.100.10/32"}})
	if !errors.Is(err, ErrProcessOwnershipUncertain) {
		t.Fatalf("Migrate = %v, want 所有权不确定", err)
	}
	if !strings.Contains(err.Error(), "identity was never verified") {
		t.Fatalf("Migrate 的锁存拒绝没有带上原因: %q", err.Error())
	}
}

// 锁存被清掉时 cause 必须一起清 —— 否则下一次拒绝会挂着上一个故事的原因,
// 那比没有原因更坏。
func TestClearingTheLatchAlsoClearsTheRetainedCause(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.store.SaveDesired(DesiredOff); err != nil {
		t.Fatal(err)
	}
	env.manager.current = Process{Uncertain: true}
	env.manager.uncertainCause = errors.New("stale story from a previous refusal")

	if err := env.manager.Down(context.Background()); err != nil {
		t.Fatalf("Down = %v, want nil", err)
	}
	if env.manager.uncertainCause != nil {
		t.Fatalf("锁存清了但 cause 留着: %v", env.manager.uncertainCause)
	}
}

// 系统里确实没有 Core 在跑 ⇒ 释放。这是整条出口存在的理由。
// **两次扫描都干净才算数** —— 断言 calls==2,而不只是「释放了」。
func TestRecheckReleasesTheLatchOnlyAfterTwoCleanScans(t *testing.T) {
	restore := coreScanSettle
	coreScanSettle = time.Millisecond
	t.Cleanup(func() { coreScanSettle = restore })

	scan := &scriptedScanner{}
	env := newManagerTestEnv(t)
	env.manager.runner = &scannerOnlyRunner{scan: scan}
	env.manager.current = Process{PID: 4242, Uncertain: true}
	env.manager.uncertainCause = errors.New("Core appears to be running (PID 4242)")

	if err := env.manager.recheckOwnershipUncertain("up"); err != nil {
		t.Fatalf("两次都扫干净了仍然拒绝: %v", err)
	}
	if env.manager.current.Uncertain || env.manager.uncertainCause != nil {
		t.Fatal("释放之后锁存或 cause 还留着")
	}
	if scan.calls != 2 {
		t.Fatalf("释放只扫了 %d 次 —— 起第二个 Core 是这个系统里最坏的结果,一次扫描不够", scan.calls)
	}
}

// **第一次扫干净、第二次扫到了 ⇒ 绝不释放。**
//
// 这正是设计点名的那个残留:子进程已 fork、Terminate() 已发、wait 超时,进程
// 既不是僵尸也还没消失,而 scanRunningCores 会跳过 argv 读不出的进程 ——
// 一次扫描完全可能给出假的「全清」。这条测试是「一次干净就够了」这个变异的
// 唯一守卫。
func TestRecheckDoesNotReleaseWhenOnlyTheFirstScanIsClean(t *testing.T) {
	restore := coreScanSettle
	coreScanSettle = time.Millisecond
	t.Cleanup(func() { coreScanSettle = restore })

	scan := &scriptedScanner{results: [][]Process{nil, {{PID: 4242}}}}
	env := newManagerTestEnv(t)
	env.manager.runner = &scannerOnlyRunner{scan: scan}
	env.manager.current = Process{PID: 4242, Uncertain: true}

	err := env.manager.recheckOwnershipUncertain("up")
	if !errors.Is(err, ErrProcessOwnershipUncertain) {
		t.Fatalf("一次假的「全清」就放行了: %v", err)
	}
	if !env.manager.current.Uncertain {
		t.Fatal("锁存被一次假的全清清掉了")
	}
	if scan.calls != 2 {
		t.Fatalf("扫了 %d 次 —— 第一次干净之后必须再扫一次才能下结论", scan.calls)
	}
}

// **第一次扫到、沉降后干净 ⇒ 同样不释放。**
//
// 这条与上一条方向相反,守的是另一个变异:「直接拿 confirmCoreStopped 来用」。
// confirmCoreStopped 在这个脚本下**会**说 stopped(它的偏置是别把正常关闭报成
// 告警,对 Down 是对的),而释放锁存是准入控制,偏置必须反过来。既有的
// TestConfirmCoreStoppedReScansAfterASettleWindow 钉住那一头,这条钉住这一头。
func TestRecheckDoesNotReleaseWhenOnlyTheSecondScanIsClean(t *testing.T) {
	restore := coreScanSettle
	coreScanSettle = time.Millisecond
	t.Cleanup(func() { coreScanSettle = restore })

	scan := &scriptedScanner{results: [][]Process{{{PID: 4242}}, nil}}
	env := newManagerTestEnv(t)
	env.manager.runner = &scannerOnlyRunner{scan: scan}
	env.manager.current = Process{PID: 4242, Uncertain: true}

	if err := env.manager.recheckOwnershipUncertain("up"); !errors.Is(err, ErrProcessOwnershipUncertain) {
		t.Fatalf("复用了 Down 那条偏置相反的求证: %v", err)
	}
	if !env.manager.current.Uncertain {
		t.Fatal("锁存被 Down 那套偏置清掉了")
	}
}

// 扫到了 ⇒ 保持拒绝,并且报出**当初**那个 cause 加上**这次**的理由。
// (第一次就扫脏,立即拒绝、不沉降,所以这条不需要缩 coreScanSettle。)
func TestRecheckKeepsRefusingWhenACoreIsStillRunning(t *testing.T) {
	env := newManagerTestEnv(t)
	env.runner.scanResult = []Process{{PID: 4242}}
	env.manager.current = Process{PID: 4242, Uncertain: true}
	env.manager.uncertainCause = errors.New("no Core process record on disk, but Core appears to be running (PID 4242)")

	err := env.manager.recheckOwnershipUncertain("up")
	if !errors.Is(err, ErrProcessOwnershipUncertain) {
		t.Fatalf("系统说还有 Core 在跑,却放行了: %v", err)
	}
	if !strings.Contains(err.Error(), "PID 4242") {
		t.Fatalf("重新求证失败时丢了当初那个 cause: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "core_still_running") {
		t.Fatalf("重新求证失败时没说清这次是怎么判的: %q", err.Error())
	}
	if !env.manager.current.Uncertain {
		t.Fatal("拒绝了却把锁存清了")
	}
	if env.manager.Status().LastError != "core_ownership_uncertain" {
		t.Fatalf("对外发布的码变了,四处消费方的指引会全部失效: %q", env.manager.Status().LastError)
	}
}

// **扫不动 ≠ 没有。** 最容易瞬时发生的一类,也是最不能塌缩成「放行」的一类。
func TestRecheckKeepsRefusingWhenTheScanFails(t *testing.T) {
	env := newManagerTestEnv(t)
	env.runner.scanErr = errors.New("sysctl failed")
	env.manager.current = Process{Uncertain: true}

	if err := env.manager.recheckOwnershipUncertain("up"); !errors.Is(err, ErrProcessOwnershipUncertain) {
		t.Fatalf("扫不动被当成了「没有」: %v", err)
	}
	if !env.manager.current.Uncertain {
		t.Fatal("扫不动却把锁存清了 —— 「问不出来」不等于「安全」")
	}
}

// runner 压根不会扫(将来别的平台、以及非 darwin 上的 procscan 桩)⇒ 保持拒绝。
func TestRecheckKeepsRefusingWhenTheRunnerCannotScan(t *testing.T) {
	env := newManagerTestEnv(t)
	env.manager.runner = &nonScanningRunner{}
	env.manager.current = Process{Uncertain: true}

	if err := env.manager.recheckOwnershipUncertain("up"); !errors.Is(err, ErrProcessOwnershipUncertain) {
		t.Fatalf("求证不了却放行: %v", err)
	}
}

// panic 也是一种失败模式,而且这一条比别的更要命:一次 panic 打死 Guardian,
// launchd 的 KeepAlive 把它拉起来再 panic —— 崩溃循环。收成「没能确认」,
// 方向与本函数其余部分一致。
func TestRecheckSurvivesAPanickingScanner(t *testing.T) {
	env := newManagerTestEnv(t)
	env.manager.runner = panickingScanner{}
	env.manager.current = Process{Uncertain: true}

	if err := env.manager.recheckOwnershipUncertain("up"); !errors.Is(err, ErrProcessOwnershipUncertain) {
		t.Fatalf("panic 之后 = %v, want 仍然拒绝(而不是让 Guardian 崩掉)", err)
	}
}

// 没有锁存时**一次扫描都不许做**。两次扫描 + 300ms 沉降是只该发生在
// 已经锁存的机器上、且由用户动作触发的开销;把它加到每一次 bx up 上,
// 就是给所有人交税。
func TestRecheckDoesNotScanWhenThereIsNoLatch(t *testing.T) {
	scan := &scriptedScanner{}
	env := newManagerTestEnv(t)
	env.manager.runner = &scannerOnlyRunner{scan: scan}

	if err := env.manager.recheckOwnershipUncertain("up"); err != nil {
		t.Fatalf("没有锁存时不该有任何拒绝: %v", err)
	}
	if scan.calls != 0 {
		t.Fatalf("没有锁存却扫了 %d 次 —— 每一次正常的 bx up 都在白交这个税", scan.calls)
	}
}

// **这次放宽本身**:用户发起的 Up 撞上锁存时,必须重新去问系统,而不是把
// 第一次的结论钉死。**两次扫描都干净** ⇒ 继续,Core 起得来。
func TestUserInitiatedUpReVerifiesAndProceedsWhenTheSystemIsClean(t *testing.T) {
	// 释放要跨一个沉降窗口(拒绝不用),缩短它免得每次跑测试都白等 300ms。
	restore := coreScanSettle
	coreScanSettle = time.Millisecond
	t.Cleanup(func() { coreScanSettle = restore })

	env := newManagerTestEnv(t)
	env.manager.current = Process{PID: 4242, Uncertain: true}
	env.manager.uncertainCause = errors.New("Core appeared to be running (PID 4242)")

	if err := env.manager.Up(context.Background()); err != nil {
		t.Fatalf("重新求证扫干净了,Up 仍然失败: %v", err)
	}
	if env.manager.Status().Protection != ProtectionProtected {
		t.Fatalf("protection = %v, want protected", env.manager.Status().Protection)
	}
	if got := env.runner.startCount(); got != 1 {
		t.Fatalf("starts = %d, want 1", got)
	}
	if got := env.runner.scanCount(); got != 2 {
		t.Fatalf("释放只扫了 %d 次 —— 一次干净不足以放行", got)
	}
}

// 放宽的另一头:系统说**还有** Core 在跑 ⇒ 照旧拒绝,而且**一个 Core 都不许起**。
// 起第二个 Core 是这个系统里最坏的结果。
// (第一次就扫脏 ⇒ 立即拒绝、不沉降,所以这条不需要缩 coreScanSettle。)
func TestUserInitiatedUpStillRefusesWhenACoreIsStillRunning(t *testing.T) {
	env := newManagerTestEnv(t)
	env.runner.scanResult = []Process{{PID: 4242}}
	env.manager.current = Process{PID: 4242, Uncertain: true}

	if err := env.manager.Up(context.Background()); !errors.Is(err, ErrProcessOwnershipUncertain) {
		t.Fatalf("Up = %v, want 仍然拒绝", err)
	}
	if got := env.runner.startCount(); got != 0 {
		t.Fatalf("拒绝时起了 %d 个 Core —— 两个 Core 争默认路由,先退出的那个用旧快照还原掀掉另一个的劫持", got)
	}
}

// 扫不动同样拒绝,同样不起 Core。
func TestUserInitiatedUpStillRefusesWhenTheScanFails(t *testing.T) {
	env := newManagerTestEnv(t)
	env.runner.scanErr = errors.New("sysctl failed")
	env.manager.current = Process{Uncertain: true}

	if err := env.manager.Up(context.Background()); !errors.Is(err, ErrProcessOwnershipUncertain) {
		t.Fatalf("Up = %v, want 仍然拒绝", err)
	}
	if got := env.runner.startCount(); got != 0 {
		t.Fatalf("扫不动却起了 %d 个 Core", got)
	}
}

// **启动恢复不许重新求证。** retryDaemonRecovery 每 5 秒重试一次、永不放弃:
// 把这套求证放进去等于一天一万七千轮,正是设计里明写「不许搬进按时钟驱动的
// 路径」的那条纪律。(「让它有限次数地自愈瞬时失败」是有价值的后续,但那要
// 照 maxPathRecoveryAttempts 的形状单独做,不是每轮都做。)
func TestStartupRecoveryDoesNotReVerifyOwnership(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.store.SaveDesired(DesiredOn); err != nil {
		t.Fatal(err)
	}
	env.manager.current = Process{Uncertain: true}

	if err := env.manager.Recover(context.Background()); !errors.Is(err, ErrProcessOwnershipUncertain) {
		t.Fatalf("Recover = %v, want 所有权不确定", err)
	}
	if got := env.runner.scanCount(); got != 0 {
		t.Fatalf("启动恢复扫了 %d 次 —— 它每 5 秒重试一次,永不放弃", got)
	}
}

// Migrate 是 bx up 在还带 legacy Core 的机器上走的那条路,同样是用户显式说的 on。
// 漏掉它就是「只修一跳」——这个仓库为这个形状付过学费(60b76f3)。
func TestMigrateReVerifiesOwnershipWhenTheSystemIsClean(t *testing.T) {
	// 释放要跨一个沉降窗口(拒绝不用)。
	restore := coreScanSettle
	coreScanSettle = time.Millisecond
	t.Cleanup(func() { coreScanSettle = restore })

	env := newManagerTestEnv(t)
	env.manager.current = Process{PID: 4242, Uncertain: true}
	env.manager.uncertainCause = errors.New("Core appeared to be running (PID 4242)")

	err := env.manager.Migrate(context.Background(), MigrationRequest{Gateway: "192.0.2.1", ServerBypass: []string{"198.51.100.10/32"}})
	if errors.Is(err, ErrProcessOwnershipUncertain) {
		t.Fatalf("扫干净了 Migrate 仍以所有权不确定拒绝: %v", err)
	}
	if env.manager.current.Uncertain {
		t.Fatal("Migrate 没有释放已被求证的锁存")
	}
	if got := env.runner.scanCount(); got != 2 {
		t.Fatalf("释放只扫了 %d 次 —— 一次干净不足以放行", got)
	}
}

// 另一头:系统说还有 Core 在跑 ⇒ 照旧拒绝,且一个 Core 都不起。
// (第一次就扫脏 ⇒ 立即拒绝、不沉降。)
func TestMigrateStillRefusesWhenACoreIsStillRunning(t *testing.T) {
	env := newManagerTestEnv(t)
	env.runner.scanResult = []Process{{PID: 4242}}
	env.manager.current = Process{PID: 4242, Uncertain: true}

	err := env.manager.Migrate(context.Background(), MigrationRequest{Gateway: "192.0.2.1", ServerBypass: []string{"198.51.100.10/32"}})
	if !errors.Is(err, ErrProcessOwnershipUncertain) {
		t.Fatalf("Migrate = %v, want 仍然拒绝", err)
	}
	if got := env.runner.startCount(); got != 0 {
		t.Fatalf("拒绝时起了 %d 个 Core", got)
	}
}

// 调谐器一侧一个字不改:循环仍然**永不**清这个锁存。既有的
// TestReconcileOnceNeverClearsTheOwnershipUncertainLatch 守着「不清」,
// 这条补上「也不去扫」——两者威胁模型不同:本期只动**用户发起**的那一半。
func TestReconcileLoopDoesNotReVerifyOwnership(t *testing.T) {
	env := newManagerTestEnv(t)
	env.manager.current = Process{Uncertain: true}
	before := env.runner.scanCount()

	_, uncertain, acquired := env.manager.readMutationFences(context.Background())
	if !acquired || !uncertain {
		t.Fatal("测试前提不成立:调谐器这一轮应当看见锁存升起")
	}
	if got := env.runner.scanCount(); got != before {
		t.Fatalf("调谐器读栅栏时扫了 %d 次(之前 %d 次)", got, before)
	}
}
