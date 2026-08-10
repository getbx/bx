package guardian

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getbx/bx/internal/observe"
	"github.com/getbx/bx/internal/version"
)

// healthyObservation 是一台与意图相符、且**每一项都问得出来**的机器
// (desired 默认为 off,所以「相符」意味着五项都观测到「没开」)。
//
// 与零值 ObservedState 的区别正是本文件的主题:零值是五项全 Unknown,也就是
// 「一个探针都没成功」,而它产出的判断(什么都不做)与这一份逐字节相同。
func healthyObservation(at time.Time) observe.ObservedState {
	return observe.ObservedState{
		ObservedAt:     at,
		CaptureOK:      observe.False,
		BarrierPresent: observe.False,
		DNSManaged:     observe.False,
		CoreSocket:     observe.False,
		TunnelHealthy:  observe.False,
	}
}

// blindObservation 是同一台机器,但每个探针都失败了。
func blindObservation(at time.Time) observe.ObservedState {
	return observe.ObservedState{
		ObservedAt: at,
		Errors: []observe.ObserveError{
			{Item: "capture_ok", Err: "route -n get: timeout"},
			{Item: "barrier_present", Err: "route -n get: timeout"},
			{Item: "dns_managed", Err: "networksetup: timeout"},
			{Item: "core_socket", Err: "dial: no such file"},
		},
	}
}

// **一台失明的机器绝不能与一台健康的机器产出同一份证据。**
//
// 这是本分支第四次遇到同一个形状(「我们从没问过」渲染得与「问了、一切正常」
// 一模一样)。判据对每一个 Unknown 都「什么都不做」,所以全盲那一轮的判断
// 恰好等于健康机器的判断:Actions 空、Held 空。只比判断的实现会让这台机器
// 从失明那一刻起再也不打一行日志,而 bx status 上写着「无差异(连续 N 轮未变)」。
//
// 两条都要:**变盲的那一刻必须打出来**(否则日志里没有任何痕迹),
// **报告必须带上是哪几项没问出来**(否则 soak 读到的仍是一份干净报告)。
func TestGoingBlindIsItselfAChangeWorthPrinting(t *testing.T) {
	env := newManagerTestEnv(t)
	logs := captureGuardianLog(t)
	stamp := time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC)

	var mu sync.Mutex
	round := 0
	reports := map[int]*ReconcileReport{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	observer := func(context.Context) observe.ObservedState {
		mu.Lock()
		defer mu.Unlock()
		reports[round] = env.manager.Status().Reconcile // 上一轮记下的那份
		round++
		at := stamp.Add(time.Duration(round) * time.Second)
		switch round {
		case 1, 2:
			return healthyObservation(at)
		case 3, 4:
			return blindObservation(at)
		default:
			cancel()
			return blindObservation(at)
		}
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		env.manager.runReconcileLoopWithPacing(ctx, observer, func(int) time.Duration { return time.Millisecond })
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("循环没有随 ctx 结束")
	}

	healthy := reports[2] // 第 2 轮开跑前读到的,即第 1 轮(健康)记下的报告
	blind := reports[4]   // 第 4 轮开跑前读到的,即第 3 轮(全盲)记下的报告
	if healthy == nil || blind == nil {
		t.Fatalf("前置条件不成立:两轮都得有报告, healthy=%+v blind=%+v", healthy, blind)
	}
	if len(healthy.Unobservable) != 0 {
		t.Errorf("一台什么都问得出来的机器不该报「未观测」, got %v", healthy.Unobservable)
	}
	if len(blind.Unobservable) == 0 {
		t.Fatal("全盲那一轮的报告没说自己什么都没看见 —— 它与一份「跑了、无差异」的报告完全一样," +
			"而 soak 的头条结论(健康机器上提议过几次动作)正好会读成 0")
	}
	// 判断本身确实相同 —— 这正是「只比判断」为什么不够。
	if len(healthy.Actions) != 0 || len(blind.Actions) != 0 || healthy.Held != "" || blind.Held != "" {
		t.Fatalf("这条测试的前提是两轮的判断一模一样, healthy=%+v blind=%+v", healthy, blind)
	}
	for _, item := range []string{"capture_ok", "barrier_present", "dns_managed", "core_socket", "tunnel_healthy"} {
		if !contains(blind.Unobservable, item) {
			t.Errorf("全盲那一轮少报了 %q, got %v", item, blind.Unobservable)
		}
	}

	lines := logLinesContaining(logs, "guardian_reconcile_would")
	if len(lines) != 2 {
		t.Fatalf("该打两行:健康的第 1 轮一行(记下基线),变盲的第 3 轮一行。got %d 行:\n%s",
			len(lines), strings.Join(lines, "\n"))
	}
	if strings.Contains(lines[0], "unobservable=") {
		t.Errorf("健康那一行不该挂 unobservable, got %q", lines[0])
	}
	if !strings.Contains(lines[1], "unobservable=capture_ok") {
		t.Errorf("变盲那一行必须说清是哪几项没问出来, got %q", lines[1])
	}
}

// 一直盲下去不该继续刷屏:变盲是一次变化,**保持盲**不是。
func TestStayingBlindDoesNotKeepPrinting(t *testing.T) {
	env := newManagerTestEnv(t)
	logs := captureGuardianLog(t)
	stamp := time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC)

	round := 0
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		env.manager.runReconcileLoopWithPacing(ctx, func(context.Context) observe.ObservedState {
			round++
			if round > 5 {
				cancel()
			}
			return blindObservation(stamp.Add(time.Duration(round) * time.Second))
		}, func(int) time.Duration { return time.Millisecond })
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("循环没有随 ctx 结束")
	}

	if lines := logLinesContaining(logs, "guardian_reconcile_would"); len(lines) != 1 {
		t.Fatalf("持续失明只该在变盲那一刻打一行,否则这个字段会被训练成噪声。got %d 行:\n%s",
			len(lines), strings.Join(lines, "\n"))
	}
}

// **循环必须自己去问「有几个进程看起来像 Core」。**
//
// 设计交付的第二样是 looksLikeCore 的真机误报率,而 scanRunningCores 今天只挂在
// Existing / Start / confirmCoreStopped 三条**改动**路径上;只观察的循环一条都不
// 会走。循环读的 m.current.Uncertain 是那些路径**锁存**下来的旧结论,不是本轮的
// 事实。于是跑上几天也攒不出一条证据。
func TestReconcileLoopMeasuresRunningCoresEveryRound(t *testing.T) {
	env := newManagerTestEnv(t)
	env.runner.mu.Lock()
	env.runner.scanResult = []Process{{PID: 111}, {PID: 222}}
	env.runner.mu.Unlock()
	before := env.mutationCallCounts()

	stamp := time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC)
	got := runTwoRoundsAndReport(t, env.manager, healthyObservation(stamp))
	if got == nil {
		t.Fatal("没有报告")
	}
	if !got.CoreScan.Measured {
		t.Fatal("循环没有做那次只读扫描 —— 这一期的第二样交付(误报率)会一天都攒不出数据")
	}
	if got.CoreScan.Cores != 2 {
		t.Errorf("扫到的进程数没记对: %+v", got.CoreScan)
	}
	if got.CoreScan.Reason != "" {
		t.Errorf("测成了就不该有「没测成的理由」: %+v", got.CoreScan)
	}
	// **测量绝不许变成动作。** 这一期一个动作都不执行,扫描也不例外。
	if after := env.mutationCallCounts(); after != before {
		t.Fatalf("测量那一跳动了系统\nbefore=%+v\nafter =%+v", before, after)
	}
}

// runTwoRoundsAndReport 跑两轮(第二轮负责收尾),返回第二轮记下的报告。
func runTwoRoundsAndReport(t *testing.T, manager *Manager, observed observe.ObservedState) *ReconcileReport {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	round := 0
	done := make(chan struct{})
	go func() {
		defer close(done)
		manager.runReconcileLoopWithPacing(ctx, func(context.Context) observe.ObservedState {
			round++
			if round >= 2 {
				cancel()
			}
			return observed
		}, func(int) time.Duration { return time.Millisecond })
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("循环没有随 ctx 结束")
	}
	return manager.Status().Reconcile
}

// **「枚举不出来」绝不能记成「一个都没有」。**
//
// 这是 decideCoreScan 那条 fail-closed 下限在测量侧的对应物:把扫描失败记成
// cores=0,算出来的误报率会偏低 —— 正好是这份测量的反面。
func TestCoreScanNeverPassesAFailureOffAsFoundNone(t *testing.T) {
	stamp := time.Date(2026, 8, 10, 11, 0, 0, 0, time.UTC)

	t.Run("scan_failed", func(t *testing.T) {
		env := newManagerTestEnv(t)
		env.runner.mu.Lock()
		env.runner.scanErr = errors.New("sysctl kern.proc: EIO")
		env.runner.mu.Unlock()

		got := runTwoRoundsAndReport(t, env.manager, healthyObservation(stamp))
		if got == nil {
			t.Fatal("没有报告")
		}
		if got.CoreScan.Measured {
			t.Fatalf("扫描报错却声称测到了: %+v", got.CoreScan)
		}
		if got.CoreScan.Reason != coreScanFailed {
			t.Errorf("没测成的理由要说清: %+v", got.CoreScan)
		}
		if got.CoreScan.Cores != 0 {
			t.Errorf("没测成时 Cores 无意义,应保持零值: %+v", got.CoreScan)
		}
	})

	t.Run("scan_unsupported", func(t *testing.T) {
		manager, err := NewManager(ManagerOptions{
			// 挂起路径必须给全:缺了它 LoadIntentSnapshot 会报错(Task 1 刻意的),
			// 于是这个 Manager 每一轮都停在 intent_unreadable 栅栏上。今天
			// measureRunningCores 跑在 reconcileOnce **之前**,所以本测试的断言
			// 照样成立 —— 但那是巧合,不是设计:任何将来加在「判断」层面的断言
			// 都会在一个永久被栅栏挡住的 Manager 上悄悄失去意义。
			Store: OpenStore(Paths{
				Desired:         t.TempDir() + "/guardian-state.json",
				MaintenanceHold: t.TempDir() + "/maintenance-hold.json",
				UpgradeIntent:   t.TempDir() + "/upgrade-intent.json",
			}),
			Runner:      &nonScanningRunner{}, // 刻意不实现 ScanRunning
			Health:      &fakeHealthGate{},
			Barrier:     &fakeBarrier{events: &eventLog{}},
			DNS:         newFakeDNSManager(&eventLog{}),
			CoreVersion: version.Version,
		})
		if err != nil {
			t.Fatal(err)
		}
		got := runTwoRoundsAndReport(t, manager, healthyObservation(stamp))
		if got == nil {
			t.Fatal("没有报告")
		}
		if got.CoreScan.Measured {
			t.Fatalf("runner 根本不会扫描,却声称测到了: %+v", got.CoreScan)
		}
		if got.CoreScan.Reason != coreScanUnsupported {
			t.Errorf("求证不了的 runner 要落到 %q, got %+v", coreScanUnsupported, got.CoreScan)
		}
	})
}

// **测量不许参与判断。** decide 的输入一个字段都没加,这里从行为上钉住:
// 无论扫到几个、还是压根没扫成,同一份观测必须产出同一个判断。
//
// 理由写在 reconcile.go 的注释里:起 Core 的准入判据是扫描,但那是阶段③b 的事,
// 而且要先解 Uncertain 锁存;这一期若让扫描影响判断,soak 的日志就不再是
// 「decide 在这份观测下会怎么做」的记录。
func TestCoreScanIsMeasurementOnlyAndNeverChangesTheVerdict(t *testing.T) {
	stamp := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	observed := healthyObservation(stamp) // desired=off 默认 ⇒ 判断为「无动作」

	verdicts := map[string]string{}
	for name, setup := range map[string]func(*fakeCoreRunner){
		"none":   func(r *fakeCoreRunner) {},
		"one":    func(r *fakeCoreRunner) { r.scanResult = []Process{{PID: 1}} },
		"three":  func(r *fakeCoreRunner) { r.scanResult = []Process{{PID: 1}, {PID: 2}, {PID: 3}} },
		"failed": func(r *fakeCoreRunner) { r.scanErr = errors.New("boom") },
	} {
		env := newManagerTestEnv(t)
		env.runner.mu.Lock()
		setup(env.runner)
		env.runner.mu.Unlock()
		got := runTwoRoundsAndReport(t, env.manager, observed)
		if got == nil {
			t.Fatalf("%s: 没有报告", name)
		}
		verdicts[name] = strings.Join(got.Actions, ",") + "|" + got.Held
	}
	first := verdicts["none"]
	for name, verdict := range verdicts {
		if verdict != first {
			t.Errorf("扫描结果改变了判断(%s=%q,none=%q)—— 测量渗进了 decide", name, verdict, first)
		}
	}
}

// 扫描约 900 次 syscall,**绝不能攥着 mutation channel 去做** ——
// 那会让用户的 `bx up` 排在它后面,与观测同一条纪律。
func TestCoreScanDoesNotHoldTheMutationChannel(t *testing.T) {
	env := newManagerTestEnv(t)
	held := make(chan struct{}, 1)
	env.runner.mu.Lock()
	env.runner.scanResult = []Process{{PID: 7}}
	env.runner.mu.Unlock()
	env.manager.runner = &mutationWatchingScanner{
		CoreRunner: env.runner,
		onScan: func() {
			select {
			case <-env.manager.mutation:
				env.manager.releaseMutation()
			default:
				held <- struct{}{}
			}
		},
	}

	stamp := time.Date(2026, 8, 10, 13, 0, 0, 0, time.UTC)
	if got := runTwoRoundsAndReport(t, env.manager, healthyObservation(stamp)); got == nil {
		t.Fatal("没有报告")
	}
	select {
	case <-held:
		t.Fatal("扫描期间 mutation channel 被持有 —— 用户的 up 会排在约 900 次 syscall 后面")
	default:
	}
}

// mutationWatchingScanner 在每次 ScanRunning 时回调一次,用来现场求证锁的归属。
type mutationWatchingScanner struct {
	CoreRunner
	onScan func()
}

func (r *mutationWatchingScanner) ScanRunning() ([]Process, error) {
	r.onScan()
	return r.CoreRunner.(coreScanner).ScanRunning()
}

// **一轮 panic 不许结束整条循环。**
//
// 上一版把 recover 放在 goroutine 那一层(trackReconcileLoop),于是第一次 panic
// 就是终点:reconcileLoopDone 关掉、没人重启,而 Status().Reconcile 冻在最后一轮
// 上被 bx status 渲染成一轮干净的观测 —— 一条死掉的循环读起来完全正常。
func TestReconcileLoopKeepsRunningAfterARoundPanics(t *testing.T) {
	env := newManagerTestEnv(t)
	logs := captureGuardianLog(t)
	stamp := time.Date(2026, 8, 10, 14, 0, 0, 0, time.UTC)

	var mu sync.Mutex
	round := 0
	var reportAfterPanic *ReconcileReport
	seenReport := false
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		env.manager.runReconcileLoopWithPacing(ctx, func(context.Context) observe.ObservedState {
			mu.Lock()
			round++
			current := round
			if current == 2 {
				// 第 1 轮炸了。**那一轮不该留下报告** —— 一轮没有产出判断的轮次
				// 若发布一份,就是编造;而 At 停住正是消费方判定「已停滞」的依据。
				reportAfterPanic = env.manager.Status().Reconcile
				seenReport = true
			}
			mu.Unlock()
			if current == 1 {
				panic("观测解析炸了")
			}
			if current >= 3 {
				cancel()
			}
			return healthyObservation(stamp.Add(time.Duration(current) * time.Second))
		}, func(int) time.Duration { return time.Millisecond })
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("循环没有随 ctx 结束")
	}

	mu.Lock()
	defer mu.Unlock()
	if !seenReport {
		t.Fatal("第一轮 panic 之后循环就没了 —— 一条死掉的循环会把最后那份报告冻在 bx status 上," +
			"读起来与一台健康机器一模一样")
	}
	if reportAfterPanic != nil {
		t.Errorf("炸掉的那一轮发布了报告 —— 它并没有做出任何判断, got %+v", reportAfterPanic)
	}
	if got := env.manager.Status().Reconcile; got == nil {
		t.Error("panic 之后的正常轮次必须照常记报告")
	}
	if lines := logLinesContaining(logs, "guardian_reconcile_round_panicked"); len(lines) != 1 {
		t.Errorf("每一次 panic 都要留下痕迹(否则一条永久 panic 的循环是静默的), got %d 行", len(lines))
	}
}

// 一个**确定性**会 panic 的轮次不许把机器烧掉:没有退避,它就是满速空转,
// 每一轮 fork 一遍观测、炸一遍、立刻再来。
func TestReconcileLoopBacksOffWhileEveryRoundPanics(t *testing.T) {
	env := newManagerTestEnv(t)
	captureGuardianLog(t) // panic 每轮一行 + 栈,别刷到测试输出里

	var mu sync.Mutex
	calls := 0
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		env.manager.runReconcileLoopWithPacing(ctx, func(context.Context) observe.ObservedState {
			mu.Lock()
			calls++
			mu.Unlock()
			panic("每一轮都炸")
		}, func(int) time.Duration { return time.Millisecond })
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("循环没有随 ctx 结束")
	}

	mu.Lock()
	defer mu.Unlock()
	if calls < 2 {
		t.Fatalf("panic 之后循环就停了, calls=%d", calls)
	}
	// 1ms 起步逐轮翻倍:1+2+4+…+256 已经超过 300ms,所以至多 9 轮左右。
	// 没有退避时同样的 300ms 里是几百轮 —— 分离度足够,慢机器只会让这个数更小。
	if calls > 30 {
		t.Fatalf("连续 panic 没有退避,300ms 里跑了 %d 轮 —— 生产上那是每轮 6 次 fork 的满速空转", calls)
	}
}

func TestReconcilePanicBackoffGrowsAndNeverShortensTheInterval(t *testing.T) {
	base := reconcileBaseInterval
	if got := reconcilePanicBackoff(base, 0); got != base {
		t.Errorf("没 panic 时不该动间隔: %v", got)
	}
	previous := base
	for consecutive := 1; consecutive <= 70; consecutive++ {
		got := reconcilePanicBackoff(base, consecutive)
		if got < previous {
			t.Fatalf("退避回缩了: reconcilePanicBackoff(%v, %d)=%v < %v", base, consecutive, got, previous)
		}
		if got > reconcileMaxInterval {
			t.Fatalf("退避越过上限: reconcilePanicBackoff(%v, %d)=%v", base, consecutive, got)
		}
		previous = got
	}
	if reconcilePanicBackoff(base, 1) <= base {
		t.Error("连续 panic 必须拉长间隔")
	}
	// pacing 已经退避到上限之后再撞 panic,「翻倍」不该把它拉回上限 —— 那是加速。
	long := reconcileMaxInterval * 3
	if got := reconcilePanicBackoff(long, 5); got != long {
		t.Errorf("已经比上限还长的间隔被 panic 退避缩短了: %v, want %v", got, long)
	}
}

// 「至多一条」必须由代码强制,不能是一句注释:第二次调用会覆写 cancel 与 done,
// 第一条循环就此失去被叫停、被等待的把手,却仍在后台按拍观测 —— 又一个孤儿。
func TestTrackReconcileLoopRefusesASecondLoop(t *testing.T) {
	d := &Daemon{}
	firstCtx := make(chan context.Context, 1)
	firstReturned := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	d.trackReconcileLoop(ctx, func(loopCtx context.Context) {
		firstCtx <- loopCtx
		<-loopCtx.Done()
		close(firstReturned)
	})
	select {
	case <-firstCtx:
	case <-time.After(5 * time.Second):
		t.Fatal("第一条循环没起来")
	}
	firstDone := d.reconcileLoopDone

	secondStarted := make(chan struct{})
	d.trackReconcileLoop(ctx, func(context.Context) { close(secondStarted) })
	select {
	case <-secondStarted:
		t.Fatal("第二次调用起了第二条循环 —— 第一条就此成为无人能叫停的孤儿")
	case <-time.After(200 * time.Millisecond):
	}
	if d.reconcileLoopDone != firstDone {
		t.Fatal("第二次调用换掉了 done channel —— Shutdown 从此等的是一条从没跑过的循环")
	}

	// 第一条仍然受管:daemon 的 cancel 依旧叫得停它。
	d.stopReconcileLoop()
	select {
	case <-firstReturned:
	case <-time.After(5 * time.Second):
		t.Fatal("第一条循环失去了被叫停的把手")
	}
	select {
	case <-d.reconcileLoopDone:
	case <-time.After(5 * time.Second):
		t.Fatal("第一条循环的收口没被记下")
	}
}

// 能力名逐字进 JSON,消费方(菜单的 StatusReport.swift)是字面比对的。
// 同一个数组里两种写法迟早有人照着旁边那条抄错。
func TestGuardianCapabilityNamesAreSnakeCase(t *testing.T) {
	if CapabilityReconcileReport != "reconcile_report" {
		t.Errorf("CapabilityReconcileReport = %q, want %q", CapabilityReconcileReport, "reconcile_report")
	}
	for _, capability := range GuardianCapabilities() {
		if strings.Contains(capability, "-") {
			t.Errorf("能力名 %q 混用了连字符,而同数组里的 %q 用下划线",
				capability, CapabilityDiagnosticsArchive)
		}
	}
}
