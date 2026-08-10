package cli

import (
	"context"
	"errors"
	"slices"
	"strings"
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

// **升级停机的三个入口都必须带着 purpose 到达。**
//
// 这一条是行为覆盖,不是「读代码看得出来」:`stop` 由 macOSDownLifecycleFor 在分支
// **之前**算出、再传给三处 forcedMacOSTeardown,而那个论证本身不构成测试。而且
// 零值恰好是错的答案 —— downPurposeUser 是 iota == 0,一个零 stopIntent 静悄悄地
// 意思是「用户要关 ⇒ 写 desired=off」。
//
// 「legacy 探测失败」这一格尤其要紧:legacyCoreMayBeRunning 在**仅仅探测失败**时
// 也返回 true(guardian.go 的三态纪律),而设计的风险一节点名的正是这条分支。
func TestUpgradeDownRecordsTheHoldOnEveryForcedEntrance(t *testing.T) {
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
			deps := teardownDeps(calls, nil, nil, nil)
			deps.client = client
			tc.arrange(&deps, client)

			result, err := macOSDownLifecycleFor(context.Background(), downPurposeUpgrade, "/etc/bx/config.yaml", deps)
			if err != nil {
				t.Fatalf("这一格本该走完强制拆除: %v", err)
			}
			if !result.Forced {
				t.Fatalf("测试前提不成立:这一格应当走强制路径,实际 %+v", result)
			}
			if len(calls.armed) == 0 {
				t.Fatalf("这个入口没有武装挂起 —— purpose 没到达它: %v", calls.order)
			}
			if calls.desiredOff != 0 {
				t.Fatalf("这个入口把升级停机写成了 desired=off(零 stopIntent 的默认答案): %v", calls.order)
			}
			if calls.cleared != 0 {
				t.Fatalf("这个入口把刚武装的挂起销掉了: %v", calls.order)
			}
		})
	}
}

// 对照组:同样三个入口,**用户**来由必须写 desired=off 并销挂起。
// 少了它,一个「无论 purpose 一律武装挂起」的实现在上面那组里照样全绿。
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
			deps := teardownDeps(calls, nil, nil, nil)
			deps.client = client
			tc.arrange(&deps, client)

			if _, err := macOSDownLifecycleFor(context.Background(), downPurposeUser, "/etc/bx/config.yaml", deps); err != nil {
				t.Fatalf("这一格本该走完强制拆除: %v", err)
			}
			if calls.desiredOff == 0 {
				t.Fatalf("用户明确要关却没写 desired=off: %v", calls.order)
			}
			if len(calls.armed) != 0 {
				t.Fatalf("用户明确要关却武装了挂起: %v", calls.order)
			}
			if calls.cleared == 0 {
				t.Fatalf("用户明确要关却没销挂起: %v", calls.order)
			}
		})
	}
}

// **干净路径上「两条都没写成」不许报成功。**
//
// 这是那个病态中间态唯一还够得着的入口:干净路径不调 forcedMacOSTeardown,
// 于是 stop.err 在这里没有第二个读者。盘上 desired=on、没有挂起,而升级的下一步
// (restartGuardianForUpgrade)会拉起一个读到 desired=on 就自己起 Core 的
// Guardian —— 二进制正换到一半。设计取舍三说这个状态必须不存在。
func TestCleanUpgradeDownDoesNotReportSuccessWhenNeitherIntentIsRecorded(t *testing.T) {
	calls := &teardownCalls{}
	client := &holdStubClient{}
	deps := teardownDeps(calls, errors.New("read-only file system"), nil, nil)
	deps.client = client
	deps.guardianReady = func(context.Context) bool { return true }
	deps.legacyLoaded = func(context.Context) (bool, error) { return false, nil }
	deps.markDesiredOff = func() error {
		calls.desiredOff++
		calls.record("desired.off")
		return errors.New("read-only file system")
	}

	result, err := macOSDownLifecycleFor(context.Background(), downPurposeUpgrade, "/etc/bx/config.yaml", deps)
	if client.downForUpgradeCalls != 1 {
		t.Fatalf("测试前提不成立:这一格必须走干净路径(DownForUpgrade 调用 %d 次,forced=%v)", client.downForUpgradeCalls, result.Forced)
	}
	if result.Forced {
		t.Fatalf("测试前提不成立:不该回落到强制路径,%+v", result)
	}
	if err == nil {
		t.Fatal("挂起与 desired=off 都没写成,却报告成功 —— 那正是设计取舍三要消灭的中间态")
	}
	if result.IntentUnrecorded == nil {
		t.Fatal("结果里必须留下痕迹:忽略 error 的调用方否则会平静地渲染成「已停止」")
	}
	// 「失败必须留下可操作线索」:两次写入都要点名,只说其中一个会把人送去查错文件。
	for _, want := range []string{"维护挂起", "desired=off"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("错误里必须点名 %q:%v", want, err)
		}
	}
	stdout, stderr := downReportLines(result)
	if !slices.ContainsFunc(stderr, func(line string) bool { return strings.Contains(line, "未能记录停机意图") }) {
		t.Fatalf("忽略 error 的调用方也必须看到这一行。stderr=%v stdout=%v", stderr, stdout)
	}
}

// **退回那一次写入必须发生在 recordStopIntent 里,而不是靠拆除的第 1 步兜住。**
//
// 干净路径是唯一能把这两者分开的地方:它根本不调 forcedMacOSTeardown,所以
// recordStopIntent 是这条路上**唯一**的写入者。把退回从 recordStopIntent 里拿掉
// (只留 fellBack: true),强制路径那几条用例照样绿 —— 第 1 步会替它写 ——
// 而这里会看到零次写入,也就是那个「既没挂起也没 desired=off」的中间态。
func TestCleanUpgradeDownFallsBackToDesiredOffWhenTheHoldWriteFails(t *testing.T) {
	calls := &teardownCalls{}
	client := &holdStubClient{}
	deps := teardownDeps(calls, errors.New("read-only file system"), nil, nil)
	deps.client = client
	deps.guardianReady = func(context.Context) bool { return true }
	deps.legacyLoaded = func(context.Context) (bool, error) { return false, nil }

	result, err := macOSDownLifecycleFor(context.Background(), downPurposeUpgrade, "/etc/bx/config.yaml", deps)
	if client.downForUpgradeCalls != 1 || result.Forced {
		t.Fatalf("测试前提不成立:这一格必须走干净路径(DownForUpgrade %d 次,forced=%v)", client.downForUpgradeCalls, result.Forced)
	}
	if err != nil {
		t.Fatalf("退回成功不是失败:%v", err)
	}
	if calls.forceTeardown != 0 {
		t.Fatalf("测试前提不成立:干净路径不该跑强制拆除,%v", calls.order)
	}
	if want := []string{"hold.arm", "desired.off"}; !slices.Equal(calls.order, want) {
		t.Fatalf("干净路径上退回那次写入没发生在 recordStopIntent 里:%v, want %v", calls.order, want)
	}
	if result.IntentUnrecorded != nil {
		t.Fatalf("退回写成了就不该留失败痕迹: %v", result.IntentUnrecorded)
	}
}

// 干净路径**写成了**意图时不许无中生有地报错 —— 反极性的对照组。
func TestCleanUpgradeDownStaysSilentWhenTheHoldIsRecorded(t *testing.T) {
	calls := &teardownCalls{}
	client := &holdStubClient{}
	deps := teardownDeps(calls, nil, nil, nil)
	deps.client = client
	deps.guardianReady = func(context.Context) bool { return true }
	deps.legacyLoaded = func(context.Context) (bool, error) { return false, nil }

	result, err := macOSDownLifecycleFor(context.Background(), downPurposeUpgrade, "/etc/bx/config.yaml", deps)
	if err != nil {
		t.Fatalf("挂起写成了就不该报错: %v", err)
	}
	if result.IntentUnrecorded != nil {
		t.Fatalf("挂起写成了就不该留失败痕迹: %v", result.IntentUnrecorded)
	}
	if len(calls.armed) != 1 || calls.desiredOff != 0 {
		t.Fatalf("干净路径上意图应当只由 recordStopIntent 武装一次: %v", calls.order)
	}
	_, stderr := downReportLines(result)
	if slices.ContainsFunc(stderr, func(line string) bool { return strings.Contains(line, "未能记录停机意图") }) {
		t.Fatalf("不该无中生有地警告: %v", stderr)
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

// **退回规则触发时必须留下痕迹,而且每一条返回路径上都要留。**
//
// 退回(挂起写不成 ⇒ 改写 desired=off)是一条**成功**路径:保护干净地停了、
// 升级照常走完、没有任何 error 产生。正因为不产生 error,不专门报一行的话它
// 就彻底无声 —— 而它的后果实打实:盘上写着「用户不想要保护」,于是一台正在
// 升级的机器与一台用户关掉了保护的机器再次长得一模一样,正是这一期要消灭的
// 那种含混。
//
// 逐条路径而不是只测一条:退回发生在所有分支**之前**,而 macOSDownLifecycleFor
// 有五个 return —— 只填其中几个正是本计划反复抓到的「改动落在旁边」。
func TestHoldFallbackIsReportedOnEveryDownPath(t *testing.T) {
	armFailure := errors.New("/var/lib/bx 不可写")
	for _, tc := range []struct {
		name    string
		arrange func(*macOSLifecycleDeps, *holdStubClient)
		forced  bool
	}{
		{
			name: "干净路径",
			arrange: func(deps *macOSLifecycleDeps, _ *holdStubClient) {
				deps.guardianReady = func(context.Context) bool { return true }
				deps.legacyLoaded = func(context.Context) (bool, error) { return false, nil }
			},
		},
		{
			name: "Guardian 不可达 ⇒ 强制拆除",
			arrange: func(deps *macOSLifecycleDeps, _ *holdStubClient) {
				deps.guardianReady = func(context.Context) bool { return false }
			},
			forced: true,
		},
		{
			name: "legacy Core 可能在跑 ⇒ 强制拆除",
			arrange: func(deps *macOSLifecycleDeps, _ *holdStubClient) {
				deps.guardianReady = func(context.Context) bool { return true }
				deps.legacyLoaded = func(context.Context) (bool, error) { return true, nil }
			},
			forced: true,
		},
		{
			name: "干净事务失败后回落 ⇒ 强制拆除",
			arrange: func(deps *macOSLifecycleDeps, client *holdStubClient) {
				deps.guardianReady = func(context.Context) bool { return true }
				deps.legacyLoaded = func(context.Context) (bool, error) { return false, nil }
				client.downErr = errors.New("guardian recovery incomplete")
			},
			forced: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := &teardownCalls{}
			client := &holdStubClient{}
			deps := teardownDeps(calls, armFailure, nil, nil)
			deps.client = client
			tc.arrange(&deps, client)

			result, _ := macOSDownLifecycleFor(context.Background(), downPurposeUpgrade, "/etc/bx/config.yaml", deps)
			if result.Forced != tc.forced {
				t.Fatalf("测试前提不成立:Forced = %v,想要 %v(%+v)", result.Forced, tc.forced, result)
			}
			if calls.desiredOff == 0 {
				t.Fatalf("测试前提不成立:这一格本该退回去写 desired=off,实际 %v", calls.order)
			}
			if result.HoldFallback == nil {
				t.Fatalf("退回了却一个字都不说:%+v", result)
			}
			if !errors.Is(result.HoldFallback, armFailure) {
				t.Fatalf("报的不是武装失败的原因,下次还会照样发生:%v", result.HoldFallback)
			}
			stdout, stderr := downReportLines(result)
			if !slices.ContainsFunc(stderr, func(line string) bool { return strings.Contains(line, "维护挂起") }) {
				t.Fatalf("用户看不到这次退回:stdout=%v stderr=%v", stdout, stderr)
			}
		})
	}
}

// 没退回就一个字都不说 —— 与这一期其余每一条新增输出同一条纪律:
// 一行常驻的提示会把它训练成噪声。
func TestNoHoldFallbackMeansNoExtraLine(t *testing.T) {
	calls := &teardownCalls{}
	client := &holdStubClient{}
	deps := teardownDeps(calls, nil, nil, nil)
	deps.client = client
	deps.guardianReady = func(context.Context) bool { return true }
	deps.legacyLoaded = func(context.Context) (bool, error) { return false, nil }

	result, err := macOSDownLifecycleFor(context.Background(), downPurposeUpgrade, "/etc/bx/config.yaml", deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls.armed) == 0 {
		t.Fatalf("测试前提不成立:这一格本该武装成功,实际 %v", calls.order)
	}
	if result.HoldFallback != nil {
		t.Fatalf("没退回却报了退回:%v", result.HoldFallback)
	}
	_, stderr := downReportLines(result)
	if slices.ContainsFunc(stderr, func(line string) bool { return strings.Contains(line, "维护挂起") }) {
		t.Fatalf("没退回却多写了一行:%v", stderr)
	}
}

// 用户显式的 down 从不退回(recordStopIntent 对 downPurposeUser 直接返回),
// 所以它永远不该出现这一行 —— 哪怕武装钩子本身是坏的。
func TestUserDownNeverReportsHoldFallback(t *testing.T) {
	calls := &teardownCalls{}
	client := &holdStubClient{}
	deps := teardownDeps(calls, errors.New("坏钩子"), nil, nil)
	deps.client = client
	deps.guardianReady = func(context.Context) bool { return true }
	deps.legacyLoaded = func(context.Context) (bool, error) { return false, nil }

	result, err := macOSDownLifecycleFor(context.Background(), downPurposeUser, "/etc/bx/config.yaml", deps)
	if err != nil {
		t.Fatal(err)
	}
	if result.HoldFallback != nil {
		t.Fatalf("用户显式的 down 不该谈维护挂起的退回:%v", result.HoldFallback)
	}
}
