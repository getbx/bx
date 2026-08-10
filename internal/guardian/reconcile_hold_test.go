package guardian

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/getbx/bx/internal/observe"
)

// 挂起是**栅栏**,不是一处待收敛的差异:整轮停摆,并说清是被哪一道挡住的。
func TestDecideHeldByMaintenanceHold(t *testing.T) {
	in := reconcileInput{
		Desired:         DesiredOff,
		MaintenanceHold: true,
		Observed: observe.ObservedState{
			CoreSocket: observe.True, BarrierPresent: observe.True, DNSManaged: observe.True,
		},
	}
	got := decide(in)
	if got.Held != heldMaintenanceHold {
		t.Fatalf("Held = %q, want %q", got.Held, heldMaintenanceHold)
	}
	if len(got.Actions) != 0 {
		t.Fatalf("被栅栏挡住的一轮不许提议动作: %v", got.Actions)
	}
}

// 「读不出意图」不是「没有挂起」,也不是「desired 是 off」——它有自己的名字。
func TestDecideHeldWhenIntentUnreadable(t *testing.T) {
	got := decide(reconcileInput{IntentUnreadable: true, Observed: observe.ObservedState{CoreSocket: observe.False}})
	if got.Held != heldIntentUnreadable || len(got.Actions) != 0 {
		t.Fatalf("decide = %+v", got)
	}
}

// 栅栏次序必须确定:同时升起时日志里出现哪一个不能随机。
func TestMaintenanceHoldRanksAfterTheThreeExistingFences(t *testing.T) {
	base := reconcileInput{Desired: DesiredOn, MaintenanceHold: true}
	base.RecoveryBlocked = true
	if got := heldBy(base); got != heldRecoveryBlocked {
		t.Fatalf("recovery_blocked 优先:%q", got)
	}
	base.RecoveryBlocked, base.PathRecoveryBusy = false, true
	if got := heldBy(base); got != heldPathRecoveryBusy {
		t.Fatalf("path_recovery 优先:%q", got)
	}
	base.PathRecoveryBusy, base.OwnershipUncertain = false, true
	if got := heldBy(base); got != heldOwnershipUncertain {
		t.Fatalf("ownership 优先:%q", got)
	}
	base.OwnershipUncertain = false
	if got := heldBy(base); got != heldMaintenanceHold {
		t.Fatalf("最后才是挂起:%q", got)
	}
}

// 「读不出意图」排在挂起**之后**:两者同时为真只可能出现在一种情形下 ——
// 一半读出来了、另一半没有,而 loadIntentSnapshot 在任何一半失败时都返回零值
// 快照,所以 MaintenanceHold 一定是 false。这条次序因此永远不影响生产输出;
// 钉住它只是为了「同时升起时报哪一个」有一个确定答案,而不是靠 switch 里
// 两个 case 的偶然先后。
func TestIntentUnreadableRanksLast(t *testing.T) {
	in := reconcileInput{Desired: DesiredOn, MaintenanceHold: true, IntentUnreadable: true}
	if got := heldBy(in); got != heldMaintenanceHold {
		t.Fatalf("挂起在前:%q", got)
	}
	in.MaintenanceHold = false
	if got := heldBy(in); got != heldIntentUnreadable {
		t.Fatalf("最后才是读不出意图:%q", got)
	}
}

// 栅栏的名字必须与**对外发布的失败码**、以及排查指引的键**逐字**相同。
//
// 三处今天都写着 intent_unreadable,而它们之间没有任何编译期联系:改其中一处
// 不会有编译错误,只会让用户拿着日志里的词去查指引一无所获(「失败必须留下
// 可操作线索」在这里退化成一条指向不存在条目的线索)。
func TestIntentUnreadableFenceMatchesThePublishedFailureCodeAndItsHint(t *testing.T) {
	if got := intentReadFailureCode(fmt.Errorf("%w: boom", ErrMaintenanceHoldUnreadable)); got != heldIntentUnreadable {
		t.Fatalf("发布的失败码 %q 与栅栏名 %q 不一致", got, heldIntentUnreadable)
	}
	if _, ok := guardianCodeHints[heldIntentUnreadable]; !ok {
		t.Fatalf("失败码 %q 没有排查指引 —— 指引的键与栅栏名已经分叉", heldIntentUnreadable)
	}
}

// 调谐器读的必须是**磁盘**,不是被 needsAttention 污染过的内存。
func TestReconcileOnceReadsDesiredFromDiskNotMemory(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.store.SaveDesired(DesiredOff); err != nil {
		t.Fatal(err)
	}
	// 让内存说 on(needsAttention 把调用方传进来的常量原样写进 status.Desired)
	env.manager.needsAttention(DesiredOn, "core_unexpected_exit")
	if env.manager.Status().Desired != DesiredOn {
		t.Fatal("前置条件不成立:内存必须真的说 on,否则这条测试证明不了它读的是磁盘")
	}

	got := env.manager.reconcileOnce(context.Background(), observe.ObservedState{
		CoreSocket: observe.True, ObservedAt: time.Now(),
	})
	if len(got.Actions) == 0 || got.Actions[0] != actionStopCore {
		t.Fatalf("盘上是 off、Core 还在跑,应提议 stop_core;实际 = %+v", got)
	}
}

// 反过来也要钉:盘上写着 on 时,判断跟着盘走。
//
// 只钉「盘是 off」那一半是不够的 —— 一个把 Desired 恒置成 DesiredOff 的实现
// (连读盘都省了)会让上面那条平凡地绿,而它在真机上意味着调谐器永远提议停掉
// 用户要的 Core。两条合起来才说明「Desired 确实来自那次读盘」。
func TestReconcileOnceFollowsDiskWhenDesiredIsOn(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.store.SaveDesired(DesiredOn); err != nil {
		t.Fatal(err)
	}
	env.manager.needsAttention(DesiredOff, "core_unexpected_exit") // 内存说 off

	got := env.manager.reconcileOnce(context.Background(), observe.ObservedState{
		CoreSocket: observe.False, ObservedAt: time.Now(),
	})
	if len(got.Actions) == 0 || got.Actions[0] != actionStartCore {
		t.Fatalf("盘上是 on、Core 不在,应提议 start_core;实际 = %+v", got)
	}
}

// 挂起武装时,连「读栅栏」这一轮也直接停摆。
func TestReconcileOnceHeldByArmedHold(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.store.SaveDesired(DesiredOn); err != nil {
		t.Fatal(err)
	}
	if err := env.store.ArmMaintenanceHold(HoldReasonUpgrade, time.Now()); err != nil {
		t.Fatal(err)
	}
	got := env.manager.reconcileOnce(context.Background(), observe.ObservedState{
		CoreSocket: observe.False, ObservedAt: time.Now(),
	})
	if got.Held != heldMaintenanceHold {
		t.Fatalf("Held = %q, want %q(否则挂起期间会提议 start_core)", got.Held, heldMaintenanceHold)
	}
	if len(got.Actions) != 0 {
		t.Fatalf("被栅栏挡住的一轮不许提议动作: %v", got.Actions)
	}
}

// 过期的挂起**不是**栅栏:它只是不再压制,判断照常做。
//
// 这条与上一条成对 —— 只有上一条时,一个「文件在就停摆」的实现同样全绿,
// 而那意味着一次崩掉的升级会把调谐器永久按住(挂起 15 分钟就过期,正是为了
// 不让这件事发生)。
func TestReconcileOnceIsNotHeldByAnExpiredHold(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.store.SaveDesired(DesiredOn); err != nil {
		t.Fatal(err)
	}
	// 武装在「一个有效期之前」,故此刻已过期。
	if err := env.store.ArmMaintenanceHold(HoldReasonUpgrade, time.Now().Add(-MaintenanceHoldDuration-time.Minute)); err != nil {
		t.Fatal(err)
	}
	got := env.manager.reconcileOnce(context.Background(), observe.ObservedState{
		CoreSocket: observe.False, ObservedAt: time.Now(),
	})
	if got.Held != "" {
		t.Fatalf("过期的挂起不该再挡住任何一轮, got Held=%q", got.Held)
	}
	if len(got.Actions) == 0 || got.Actions[0] != actionStartCore {
		t.Fatalf("过期之后判断照常做:desired=on、Core 不在 ⇒ start_core;实际 = %+v", got)
	}
}

// 意图读不出来时,reconcileOnce 必须报第五道栅栏 —— 不许塌缩成 off(去停一个
// 用户要的 Core)也不许塌缩成 on(在维护窗口里起一个不该起的)。
//
// **这条必须在 reconcileOnce 这一层测**:decide 那条(TestDecideHeldWhenIntentUnreadable)
// 只证明「字段为真时会停摆」,证明不了 reconcileOnce 在读盘失败时**会把那个字段
// 置真** —— 把错误分支写成「当作 desired=off 继续」,decide 那条照样绿。
func TestReconcileOnceHeldWhenIntentIsUnreadable(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.store.SaveDesired(DesiredOff); err != nil {
		t.Fatal(err)
	}
	env.store.setLoadError(errors.New("read desired state: permission denied"))

	got := env.manager.reconcileOnce(context.Background(), observe.ObservedState{
		// 盘上写的是 off 而 DNS/屏障/Core 都还在:若把读失败当成 off 继续,
		// 这一轮会提议三个动作,与「停摆」在输出上截然不同。
		CoreSocket: observe.True, BarrierPresent: observe.True, DNSManaged: observe.True,
		ObservedAt: time.Now(),
	})
	if got.Held != heldIntentUnreadable {
		t.Fatalf("Held = %q, want %q", got.Held, heldIntentUnreadable)
	}
	if len(got.Actions) != 0 {
		t.Fatalf("读不出意图的一轮不许提议动作: %v", got.Actions)
	}
}

// 读不出意图时**不许去排 mutation 的队**:与另外两道「读得到」的栅栏同例,
// 一轮注定跳过的判断没有理由挤在用户的 up 前面。
func TestReconcileOnceDoesNotTakeTheMutationChannelWhenIntentIsUnreadable(t *testing.T) {
	env := newManagerTestEnv(t)
	env.store.setLoadError(errors.New("read desired state: permission denied"))

	// 把锁攥在手里:若 reconcileOnce 仍去取它,下面会等满超时才回来。
	if err := env.manager.acquireMutation(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer env.manager.releaseMutation()

	done := make(chan reconcileDecision, 1)
	go func() {
		done <- env.manager.reconcileOnce(context.Background(), observe.ObservedState{DNSManaged: observe.True})
	}()
	select {
	case got := <-done:
		if got.Held != heldIntentUnreadable {
			t.Fatalf("Held = %q, want %q", got.Held, heldIntentUnreadable)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("意图读不出来时不该再去排 mutation 的队")
	}
}

// 挂起武装时同样不去排队。
func TestReconcileOnceDoesNotTakeTheMutationChannelUnderArmedHold(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.store.ArmMaintenanceHold(HoldReasonUpgrade, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := env.manager.acquireMutation(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer env.manager.releaseMutation()

	done := make(chan reconcileDecision, 1)
	go func() {
		done <- env.manager.reconcileOnce(context.Background(), observe.ObservedState{DNSManaged: observe.True})
	}()
	select {
	case got := <-done:
		if got.Held != heldMaintenanceHold {
			t.Fatalf("Held = %q, want %q", got.Held, heldMaintenanceHold)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("挂起武装时不该再去排 mutation 的队")
	}
}

// 两道新栅栏一样**只判断、不动手**:挂起期间去「收敛」正是它要拦的那件事。
//
// 与 TestReconcileOnceExecutesNothing 不重复 —— 那条走的是无栅栏的正常路,
// 而这条走的是新加的两条短路,它们在 decide 之前就返回了。
func TestReconcileOnceExecutesNothingUnderTheNewFences(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*testing.T, *managerTestEnv)
	}{
		{"armed_hold", func(t *testing.T, env *managerTestEnv) {
			if err := env.store.ArmMaintenanceHold(HoldReasonUpgrade, time.Now()); err != nil {
				t.Fatal(err)
			}
		}},
		{"intent_unreadable", func(t *testing.T, env *managerTestEnv) {
			env.store.setLoadError(errors.New("read desired state: EIO"))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newManagerTestEnv(t)
			tc.setup(t, env)
			before := env.mutationCallCounts()

			got := env.manager.reconcileOnce(context.Background(), observe.ObservedState{
				DNSManaged: observe.True, BarrierPresent: observe.True, CoreSocket: observe.False,
			})
			if got.Held == "" {
				t.Fatalf("前置条件不成立:这一轮本该被栅栏挡住, got %+v", got)
			}
			if after := env.mutationCallCounts(); after != before {
				t.Fatalf("被栅栏挡住的一轮更不许动任何东西\nbefore=%+v\nafter =%+v", before, after)
			}
		})
	}
}
