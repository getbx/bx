package cli

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/getbx/bx/internal/guardian"
)

// teardownCalls 既数次数也记顺序。顺序不是装饰:挂起必须**先于**停 Core 落盘,
// 否则一个还活着的 Guardian 会把 Core 的退出当成崩溃、把它重启回来
// (handleUnexpectedExit),而拦住它的正是这张挂起。只数次数的话,一个
// 「六步做完之后才武装」的实现照样绿。
type teardownCalls struct {
	order         []string
	armed         []string
	cleared       int
	desiredOff    int
	stopCore      int
	forceTeardown int
	barrier       int
	dns           int
}

func (c *teardownCalls) record(event string) { c.order = append(c.order, event) }

func teardownDeps(calls *teardownCalls, armErr, stopErr, dnsErr error) macOSLifecycleDeps {
	return macOSLifecycleDeps{
		guardianReady: func(context.Context) bool { return false }, // 直奔强制拆除
		armMaintenanceHold: func(reason string) error {
			calls.armed = append(calls.armed, reason)
			calls.record("hold.arm")
			return armErr
		},
		clearMaintenanceHold: func() error { calls.cleared++; calls.record("hold.clear"); return nil },
		markDesiredOff:       func() error { calls.desiredOff++; calls.record("desired.off"); return nil },
		stopCore:             func(context.Context) error { calls.stopCore++; calls.record("core.stop"); return stopErr },
		forceTeardown:        func(context.Context) error { calls.forceTeardown++; calls.record("guardian.bootout"); return nil },
		clearBarrierRoutes:   func(context.Context) error { calls.barrier++; calls.record("barrier.clear"); return nil },
		restoreSystemDNS:     func(context.Context) error { calls.dns++; calls.record("dns.restore"); return dnsErr },
	}
}

// 升级的停机不再写 desired=off —— 磁盘上那句「用户不想要保护」正是要消灭的谎话。
func TestForcedTeardownForUpgradeArmsHoldInsteadOfWritingDesiredOff(t *testing.T) {
	calls := &teardownCalls{}
	if _, err := macOSDownLifecycleFor(context.Background(), downPurposeUpgrade, "/etc/bx/config.yaml", teardownDeps(calls, nil, nil, nil)); err != nil {
		t.Fatal(err)
	}
	if len(calls.armed) == 0 || calls.armed[0] != guardian.HoldReasonUpgrade {
		t.Fatalf("没有武装挂起: %v", calls.armed)
	}
	if calls.desiredOff != 0 {
		t.Fatalf("升级路径写了 %d 次 desired=off", calls.desiredOff)
	}
	arm, stop := slices.Index(calls.order, "hold.arm"), slices.Index(calls.order, "core.stop")
	if arm < 0 || stop < 0 || arm > stop {
		t.Fatalf("挂起必须先于停 Core 落盘,否则活着的 Guardian 会把 Core 重启回来: %v", calls.order)
	}
	// 升级不是「用户不要保护了」,不许顺手把自己前一秒武装的挂起销掉。
	if calls.cleared != 0 {
		t.Fatalf("升级路径销了 %d 次挂起", calls.cleared)
	}
}

// **设计取舍三,逃生路径的不变量**:挂起写失败时退回写 desired=off,
// 而且六步破坏性动作照常全做完 —— 停止永不依赖别的先成功。
func TestForcedTeardownFallsBackToDesiredOffWhenHoldWriteFailsAndStillTearsDown(t *testing.T) {
	calls := &teardownCalls{}
	deps := teardownDeps(calls, errors.New("read-only file system"), nil, nil)
	if _, err := macOSDownLifecycleFor(context.Background(), downPurposeUpgrade, "/etc/bx/config.yaml", deps); err != nil {
		t.Fatalf("挂起写失败不该让拆除整个失败: %v", err)
	}
	if calls.desiredOff == 0 {
		t.Fatal("挂起写失败必须退回 desired=off:既没挂起也没 off 是一个新的失效模式")
	}
	if calls.stopCore == 0 || calls.forceTeardown == 0 || calls.barrier == 0 || calls.dns == 0 {
		t.Fatalf("破坏性步骤没做完: %+v", calls)
	}
	// 退回来的那句 off 也必须先于停 Core —— 它接手的正是挂起本该守的那个位置。
	off, stop := slices.Index(calls.order, "desired.off"), slices.Index(calls.order, "core.stop")
	if off < 0 || stop < 0 || off > stop {
		t.Fatalf("退回的 desired=off 必须先于停 Core: %v", calls.order)
	}
}

// 退回之后**不许再回头去武装挂起**:那半张写不成的挂起若在第 6 步侥幸写成,
// 盘上就会同时躺着「用户不想要保护」和「此刻不该有保护」两句话,而调谐器与
// handleUnexpectedExit 各读一句。退回是一次性的决定,不是可以反悔的尝试。
func TestForcedTeardownFallbackDoesNotRetryArmingTheHold(t *testing.T) {
	calls := &teardownCalls{}
	deps := teardownDeps(calls, errors.New("read-only file system"), nil, nil)
	if _, err := macOSDownLifecycleFor(context.Background(), downPurposeUpgrade, "/etc/bx/config.yaml", deps); err != nil {
		t.Fatal(err)
	}
	if len(calls.armed) != 1 {
		t.Fatalf("退回之后不该再试着武装挂起,实际武装了 %d 次", len(calls.armed))
	}
	// 退回之后走的就是今天那条路,逐字钉住:试一次挂起 → 退回写 off → 拆除的
	// 第 1 步再写一次(盖住并发的 Up)→ 六步 → bootout 之后那次权威的写入。
	want := []string{
		"hold.arm", "desired.off",
		"desired.off", "core.stop", "guardian.bootout", "barrier.clear", "dns.restore", "desired.off",
	}
	if !slices.Equal(calls.order, want) {
		t.Fatalf("退回之后的动作序列 = %v, want %v", calls.order, want)
	}
}

// **欠条那个活 bug 的回归**:用户明确要关,清挂起对拆除的成败必须无条件 ——
// forcedMacOSTeardown 即使报告失败,六步也已经做完了(upgradeplan.go:110-118)。
func TestForcedTeardownForUserClearsHoldEvenWhenStepsFail(t *testing.T) {
	calls := &teardownCalls{}
	deps := teardownDeps(calls, nil, errors.New("core unreachable"), errors.New("dns restore timed out"))
	if _, err := macOSDownLifecycleFor(context.Background(), downPurposeUser, "/etc/bx/config.yaml", deps); err == nil {
		t.Fatal("这一轮本该报告失败")
	}
	if calls.cleared == 0 {
		t.Fatal("拆除报错就跳过销挂起 —— 正是 upgrade-intent.json 今天留下陈旧记录的原因")
	}
	if calls.desiredOff == 0 {
		t.Fatal("用户显式关闭仍要写 desired=off")
	}
	if len(calls.armed) != 0 {
		t.Fatalf("用户明确要关,绝不许顺手武装一张挂起: %v", calls.armed)
	}
}

// 清挂起失败只是一条警告,绝不许据此中止剩下的拆除步骤 ——
// ClearMaintenanceHold 只有 ENOENT 是幂等的,EACCES/EIO 照样回错误。
func TestForcedTeardownContinuesWhenClearingTheHoldFails(t *testing.T) {
	calls := &teardownCalls{}
	deps := teardownDeps(calls, nil, nil, nil)
	deps.clearMaintenanceHold = func() error {
		calls.cleared++
		calls.record("hold.clear")
		return errors.New("permission denied")
	}
	_, err := macOSDownLifecycleFor(context.Background(), downPurposeUser, "/etc/bx/config.yaml", deps)
	if err == nil {
		t.Fatal("清不掉挂起要如实汇报")
	}
	if calls.stopCore == 0 || calls.forceTeardown == 0 || calls.barrier == 0 || calls.dns == 0 {
		t.Fatalf("清挂起失败把拆除中断了: %+v", calls)
	}
}

// 挂起钩子没接(非 darwin、或某个替身漏了它)时,升级路径必须退回 desired=off,
// 而不是安静地既不挂起也不写 off —— 后者正是那个新的失效模式。
func TestForcedTeardownWithoutHoldHookFallsBackToDesiredOff(t *testing.T) {
	calls := &teardownCalls{}
	deps := teardownDeps(calls, nil, nil, nil)
	deps.armMaintenanceHold = nil
	if _, err := macOSDownLifecycleFor(context.Background(), downPurposeUpgrade, "/etc/bx/config.yaml", deps); err != nil {
		t.Fatal(err)
	}
	if calls.desiredOff == 0 {
		t.Fatal("没有挂起钩子时必须退回今天的行为")
	}
}
