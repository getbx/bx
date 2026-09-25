package cli

import (
	"context"
	"errors"
	"testing"

	"github.com/getbx/bx/internal/guardian"
)

// teardownCalls 既数次数也记顺序(顺序进失败信息,方便看出走了哪条路)。拆除步骤的
// 顺序本身 —— desired=off 先于停 Core 落盘 —— 由
// TestMacOSDownForcedTeardownStopsCoreClearsBarrierAndPersistsOff 逐字钉住。
type teardownCalls struct {
	order         []string
	cleared       int
	desiredOff    int
	stopCore      int
	forceTeardown int
	barrier       int
	dns           int
}

func (c *teardownCalls) record(event string) { c.order = append(c.order, event) }

func teardownDeps(calls *teardownCalls, stopErr, dnsErr error) macOSLifecycleDeps {
	return macOSLifecycleDeps{
		guardianReady:        func(context.Context) bool { return false }, // 直奔强制拆除
		clearMaintenanceHold: func() error { calls.cleared++; calls.record("hold.clear"); return nil },
		markDesiredOff:       func() error { calls.desiredOff++; calls.record("desired.off"); return nil },
		stopCore:             func(context.Context) error { calls.stopCore++; calls.record("core.stop"); return stopErr },
		forceTeardown:        func(context.Context) error { calls.forceTeardown++; calls.record("guardian.bootout"); return nil },
		clearBarrierRoutes:   func(context.Context) error { calls.barrier++; calls.record("barrier.clear"); return nil },
		restoreSystemDNS:     func(context.Context) error { calls.dns++; calls.record("dns.restore"); return dnsErr },
	}
}

// holdStubClient 是一个只够 macOSDownLifecycleFor 走完干净路径的 Guardian 客户端。
// 它刻意住在本文件(无 build tag):recordingGuardianClient 是 darwin-only 的,
// 而这一组用例验的是与平台无关的编排。
type holdStubClient struct {
	downForUpgradeCalls int
	downCalls           int
	downErr             error
}

func (c *holdStubClient) Status(context.Context) (guardian.Status, error) {
	return guardian.Status{}, nil
}

func (c *holdStubClient) Up(context.Context) (guardian.Status, error) {
	return guardian.Status{}, errors.New("holdStubClient 不支持 Up")
}

func (c *holdStubClient) Down(context.Context) (guardian.Status, error) {
	c.downCalls++
	return guardian.Status{Protection: guardian.ProtectionOff}, c.downErr
}

func (c *holdStubClient) DownForUpgrade(context.Context) (guardian.Status, error) {
	c.downForUpgradeCalls++
	return guardian.Status{Protection: guardian.ProtectionOff}, c.downErr
}

func (c *holdStubClient) Migrate(context.Context, guardian.MigrationRequest) (guardian.Status, error) {
	return guardian.Status{}, errors.New("holdStubClient 不支持 Migrate")
}

// **升级停机的每个强制入口都必须带着 purpose 到达。**
//
// 这一条是行为覆盖,不是「读代码看得出来」:purpose 由 macOSDownLifecycleFor 传给
// 三处 forcedMacOSTeardown,而那个论证本身不构成测试。而且零值恰好是错的答案 ——
// downPurposeUser 是 iota == 0,一个零 purpose 静悄悄地意思是「用户要关 ⇒ 写
// desired=off、销挂起」。
//
// 「legacy 探测失败」这一格尤其要紧:legacyCoreMayBeRunning 在**仅仅探测失败**时
// 也返回 true(guardian.go 的三态纪律),而设计的风险一节点名的正是这条分支。
func TestUnprotectedUpgradeDownRecordsNothingOnEveryForcedEntrance(t *testing.T) {
	probeFailure := errors.New("launchctl print 卡住")
	for _, tc := range []struct {
		name    string
		arrange func(*macOSLifecycleDeps, *holdStubClient)
	}{
		{
			name: "Guardian 不可达",
			arrange: func(deps *macOSLifecycleDeps, _ *holdStubClient) {
				deps.guardianReady = func(context.Context) bool { return false }
			},
		},
		{
			name: "legacy Core 可能在跑",
			arrange: func(deps *macOSLifecycleDeps, _ *holdStubClient) {
				deps.guardianReady = func(context.Context) bool { return true }
				deps.legacyLoaded = func(context.Context) (bool, error) { return true, nil }
			},
		},
		{
			name: "legacy 探测本身失败(仅仅问不出来也会走强制路径)",
			arrange: func(deps *macOSLifecycleDeps, _ *holdStubClient) {
				deps.guardianReady = func(context.Context) bool { return true }
				deps.legacyLoaded = func(context.Context) (bool, error) { return false, probeFailure }
			},
		},
		{
			name: "干净事务失败后回落",
			arrange: func(deps *macOSLifecycleDeps, client *holdStubClient) {
				deps.guardianReady = func(context.Context) bool { return true }
				deps.legacyLoaded = func(context.Context) (bool, error) { return false, nil }
				client.downErr = errors.New("guardian recovery incomplete")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := &teardownCalls{}
			client := &holdStubClient{}
			deps := teardownDeps(calls, nil, nil)
			deps.client = client
			tc.arrange(&deps, client)

			result, err := macOSDownLifecycleFor(context.Background(), downPurposeUpgradeUnprotected, "/etc/bx/config.yaml", deps)
			if err != nil {
				t.Fatalf("这一格本该走完强制拆除: %v", err)
			}
			if !result.Forced {
				t.Fatalf("测试前提不成立:这一格应当走强制路径,实际 %+v", result)
			}
			if calls.stopCore == 0 || calls.forceTeardown == 0 {
				t.Fatalf("测试前提不成立:拆除没做: %v", calls.order)
			}
			if calls.desiredOff != 0 {
				t.Fatalf("这个入口把升级停机写成了 desired=off(零 purpose 的默认答案): %v", calls.order)
			}
			if calls.cleared != 0 {
				t.Fatalf("这个入口把升级停机当成了用户的关闭、销了挂起: %v", calls.order)
			}
		})
	}
}

// 对照组:同样的入口,**用户**来由必须写 desired=off 并销挂起。
// 少了它,一个「无论 purpose 一律什么都不写」的实现在上面那组里照样全绿。
func TestUserDownRecordsDesiredOffOnEveryForcedEntrance(t *testing.T) {
	for _, tc := range []struct {
		name    string
		arrange func(*macOSLifecycleDeps, *holdStubClient)
	}{
		{
			name: "Guardian 不可达",
			arrange: func(deps *macOSLifecycleDeps, _ *holdStubClient) {
				deps.guardianReady = func(context.Context) bool { return false }
			},
		},
		{
			name: "legacy Core 可能在跑",
			arrange: func(deps *macOSLifecycleDeps, _ *holdStubClient) {
				deps.guardianReady = func(context.Context) bool { return true }
				deps.legacyLoaded = func(context.Context) (bool, error) { return true, nil }
			},
		},
		{
			name: "干净事务失败后回落",
			arrange: func(deps *macOSLifecycleDeps, client *holdStubClient) {
				deps.guardianReady = func(context.Context) bool { return true }
				deps.legacyLoaded = func(context.Context) (bool, error) { return false, nil }
				client.downErr = errors.New("guardian recovery incomplete")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := &teardownCalls{}
			client := &holdStubClient{}
			deps := teardownDeps(calls, nil, nil)
			deps.client = client
			tc.arrange(&deps, client)

			if _, err := macOSDownLifecycleFor(context.Background(), downPurposeUser, "/etc/bx/config.yaml", deps); err != nil {
				t.Fatalf("这一格本该走完强制拆除: %v", err)
			}
			if calls.desiredOff == 0 {
				t.Fatalf("用户明确要关却没写 desired=off: %v", calls.order)
			}
			if calls.cleared == 0 {
				t.Fatalf("用户明确要关却没销挂起: %v", calls.order)
			}
		})
	}
}

// **一台本来就不要保护的机器,升级不许在它身上武装挂起。**
//
// 没有任何东西会去清它:upgradeSteps(running, desiredOn=false) 里根本没有
// 「恢复保护」这一步,而销挂起只发生在用户显式的 up/down/migrate 上。于是那
// 15 分钟里菜单表头写着 Paused、bx status 写着「保护此刻被有意压制」,而机器
// 关着只是因为用户想关着 —— 还会让启动恢复走 HoldArmed 那一支、跳过
// desired==off 那支的 restoreDNS,把陈旧的托管 DNS 留在系统里。
func TestUpgradeDownOnAMachineThatWantsProtectionOffArmsNoHold(t *testing.T) {
	calls := &teardownCalls{}
	deps := teardownDeps(calls, nil, nil)
	if _, err := macOSDownLifecycleFor(context.Background(), downPurposeUpgradeUnprotected, "/etc/bx/config.yaml", deps); err != nil {
		t.Fatal(err)
	}
	// 武装挂起这件事 CLI 的停机路径按构造做不到(macOSLifecycleDeps 没有那个钩子);
	// 这里钉的是剩下两件:拆除照做完(这一步的职责没变),以及不当成用户的关闭。
	if calls.stopCore == 0 || calls.forceTeardown == 0 || calls.barrier == 0 || calls.dns == 0 {
		t.Fatalf("破坏性步骤没做完: %+v", calls)
	}
	// 也不许顺手当成「用户要关」:那会销掉一张可能存在的挂起、写一次 desired=off
	// —— 而 desired 只由用户改。
	if calls.cleared != 0 || calls.desiredOff != 0 {
		t.Fatalf("升级不是用户显式关闭:cleared=%d desiredOff=%d %v", calls.cleared, calls.desiredOff, calls.order)
	}
}

// 干净路径上这条来由同样必须走 DownForUpgrade:普通的 Down 会被 Guardian 当作
// 「用户不要保护了」——写 desired=off、销挂起,两件都不该发生。
func TestUpgradeDownOnAnUnprotectedMachineStillUsesTheMaintenanceEndpoint(t *testing.T) {
	calls := &teardownCalls{}
	client := &holdStubClient{}
	deps := teardownDeps(calls, nil, nil)
	deps.client = client
	deps.guardianReady = func(context.Context) bool { return true }
	deps.legacyLoaded = func(context.Context) (bool, error) { return false, nil }

	if _, err := macOSDownLifecycleFor(context.Background(), downPurposeUpgradeUnprotected, "/etc/bx/config.yaml", deps); err != nil {
		t.Fatal(err)
	}
	if client.downForUpgradeCalls != 1 || client.downCalls != 0 {
		t.Fatalf("走错了端点:DownForUpgrade=%d Down=%d", client.downForUpgradeCalls, client.downCalls)
	}
}

// desired=off 在第 1 步写失败只是记在一边:第 6 步(Guardian 已被 bootout,那次写入是
// 权威的)再写一次成功就把它清掉,整条拆除照样报成功。
func TestForcedTeardownRecoversFromAFirstStepDesiredWriteFailure(t *testing.T) {
	calls := &teardownCalls{}
	deps := teardownDeps(calls, nil, nil)
	failFirst := true
	deps.markDesiredOff = func() error {
		calls.desiredOff++
		calls.record("desired.off")
		if failFirst {
			failFirst = false
			return errors.New("暂时写不进去")
		}
		return nil
	}

	if _, err := macOSDownLifecycleFor(context.Background(), downPurposeUser, "/etc/bx/config.yaml", deps); err != nil {
		t.Fatalf("第 6 步补写成功之后不该再报错: %v", err)
	}
	if calls.desiredOff < 2 {
		t.Fatalf("测试前提不成立:第 6 步必须再写一次,实际 %v", calls.order)
	}
}

// **欠条那个活 bug 的回归**:用户明确要关,清挂起对拆除的成败必须无条件 ——
// forcedMacOSTeardown 即使报告失败,六步也已经做完了(upgradeplan.go:110-118)。
func TestForcedTeardownForUserClearsHoldEvenWhenStepsFail(t *testing.T) {
	calls := &teardownCalls{}
	deps := teardownDeps(calls, errors.New("core unreachable"), errors.New("dns restore timed out"))
	if _, err := macOSDownLifecycleFor(context.Background(), downPurposeUser, "/etc/bx/config.yaml", deps); err == nil {
		t.Fatal("这一轮本该报告失败")
	}
	if calls.cleared == 0 {
		t.Fatal("拆除报错就跳过销挂起 —— 正是 upgrade-intent.json 今天留下陈旧记录的原因")
	}
	if calls.desiredOff == 0 {
		t.Fatal("用户显式关闭仍要写 desired=off")
	}
}

// 清挂起失败只是一条警告,绝不许据此中止剩下的拆除步骤 ——
// ClearMaintenanceHold 只有 ENOENT 是幂等的,EACCES/EIO 照样回错误。
func TestForcedTeardownContinuesWhenClearingTheHoldFails(t *testing.T) {
	calls := &teardownCalls{}
	deps := teardownDeps(calls, nil, nil)
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
