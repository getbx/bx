package guardian

import (
	"context"
	"errors"
	"testing"
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
