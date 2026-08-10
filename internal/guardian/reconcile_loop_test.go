package guardian

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getbx/bx/internal/observe"
)

// mutationCallCounts 是「系统被动过什么」的一份行为快照。
//
// 本期的核心约束(一个动作都不许执行)只能用**行为**守:本仓库的源码文本/AST
// 守卫上一轮被绕过了八次(拼写而非身份、邻近注释满足断言、行为一致的重实现……),
// 所以这里断言的不是「代码里没写 restoreDNS 这几个字」,而是「跑完一轮之后,
// 注入的替身身上一次改动都没有发生」。
//
// 主判据是 managerTestEnv 里那本**共享**事件日志:它比逐个数选定的 hook 更强 ——
// 一次经由我没想到去数的路径发生的改动照样会被记进去(core.start/core.stop、
// barrier.install/reassert/release/remove、legacy.stop/remove、desired.on/off、
// dns.ensure/inspect/restore 全在里面)。
//
// 另外三样是**独立于事件日志**的第二证人,专防「日志这条线自己坏掉」:
//   - fakeDNSManager 默认不写日志(record 关着),所以这里显式打开它 —— 变异①
//     (让 reconcileOnce 真去调 restoreDNS)若只靠日志而 record 没开,就会得到
//     一条永远绿的假守卫,正是本轮要避免的那种东西;
//   - dnsStatus 是 restoreDNS 经 cacheDNSStatus 一定会改的内存状态,与 record
//     无关;
//   - Status/所有权/屏障凭证覆盖那些不落事件日志的改动(needsAttention、
//     SetExecutable、m.current 被改写)。
type mutationCallCounts struct {
	Events         string
	CoreStarts     int
	CoreStops      int
	CoreWatches    int
	CoreExecutable string
	CorePID        int
	CoreUncertain  bool
	BarrierProof   barrierProof
	DNSState       DNSState
	DNSService     string
	Status         string
}

func (e *managerTestEnv) mutationCallCounts() mutationCallCounts {
	// 见上:替身默认不记 DNS 事件,不打开它这条守卫就是假绿的。
	e.dns.mu.Lock()
	e.dns.record = true
	e.dns.mu.Unlock()

	manager := e.manager
	return mutationCallCounts{
		Events:         strings.Join(e.events.snapshot(), ","),
		CoreStarts:     e.runner.startCount(),
		CoreStops:      e.runner.signalCount(),
		CoreWatches:    e.runner.watchCount(),
		CoreExecutable: e.runner.Executable(),
		CorePID:        manager.current.PID,
		CoreUncertain:  manager.current.Uncertain,
		BarrierProof:   manager.barrierOwnership.proof,
		DNSState:       manager.dnsStatus.State,
		DNSService:     manager.dnsStatus.Service,
		Status:         fmt.Sprintf("%+v", manager.Status()),
	}
}

// **本期的核心约束:一个动作都不许执行。**
//
// 判据会提议 restore_dns / clear_orphan_barrier / start_core,而本期它们只该进日志。
// 这条用行为断言守 —— 注入的 fake 上所有 mutating hook 调用次数必须为 0 ——
// 而不是读源码文本:本仓库的文本守卫这一轮被绕过了八次。
func TestReconcileOnceExecutesNothing(t *testing.T) {
	env := newManagerTestEnv(t)
	before := env.mutationCallCounts()

	got := env.manager.reconcileOnce(context.Background(), observe.ObservedState{
		DNSManaged:     observe.True,
		BarrierPresent: observe.True,
		CoreSocket:     observe.False,
	})
	if len(got.Actions) == 0 {
		t.Fatal("这份观测本该提议动作,否则这条测试证明不了「提议了但没执行」")
	}
	if after := env.mutationCallCounts(); after != before {
		t.Fatalf("只观察阶段绝不许执行任何动作\nbefore=%+v\nafter =%+v", before, after)
	}
}

// **循环唯一的生产入口是 runReconcileLoop,而上面那条测试走的是 WithPacing。**
//
// 这不是重复:复审把 `_ = m.restoreDNS(ctx)` 与 `_ = m.store.SaveDesired(DesiredOn)`
// 放进 runReconcileLoop 的函数体,**整个 internal/guardian 套件照样全绿** ——
// 本期最重要的那条属性守在了生产根本不调用的那个函数上。那层包装看着只有一行
// (转调 WithPacing 并注入真实节奏),但「看着只有一行」正是这类洞的长相。
//
// ctx 预先取消 ⇒ waitReconcileInterval 立刻返回 ⇒ 一轮都不跑,于是这条测试
// 覆盖的恰好是包装本身:包装里任何一句副作用都会在这里现形。
func TestRunReconcileLoopExecutesNothingOnTheProductionEntryPoint(t *testing.T) {
	env := newManagerTestEnv(t)
	before := env.mutationCallCounts()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		env.manager.runReconcileLoop(ctx, func(context.Context) observe.ObservedState {
			t.Error("ctx 已取消,一轮都不该观测")
			return observe.ObservedState{}
		})
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		// 生产入口注入的是真实节奏(基础 30s),ctx 已取消时它必须立刻返回而不是先睡。
		t.Fatal("ctx 已取消时生产入口没有立刻返回")
	}

	if after := env.mutationCallCounts(); after != before {
		t.Fatalf("生产入口那层包装里也不许有任何副作用\nbefore=%+v\nafter =%+v", before, after)
	}
}

// 观测连续相同就退避:一整轮观测在 darwin 上是 6 次 fork 加约 900 次 syscall,
// 按拍烧掉它没有意义,而且会让日志变成噪声。
func TestNextReconcileIntervalBacksOffWhenNothingChanges(t *testing.T) {
	first := nextReconcileInterval(0)
	if first < 30*time.Second {
		t.Fatalf("基础周期不该短于 30s(一轮观测 6 次 fork), got %v", first)
	}
	if nextReconcileInterval(10) <= first {
		t.Error("连续无变化时必须退避")
	}

	// **上限必须逐轮扫,不能只抽查一个大轮数。**
	//
	// 复审实测:把上限那句 early return 删掉之后,`nextReconcileInterval(1000)`
	// 返回的是 **0** —— time.Duration 是 int64,轮到第 54 轮就已经翻越 int64
	// 上界、乘成 0,此后恒 0。于是「只查 got > 10min」的那条抽查在一个**没有上限**
	// 的实现上照样绿,而且是以最坏的方式绿:它测到的其实是溢出。
	// 中间那段(第 5 轮 16 分、第 10 轮 8 小时半、第 20 轮 364 天)从来没人看过。
	//
	// 逐轮扫、上下界都钉:上界抓「睡死了」,下界抓「溢出成 0 / 回绕成负」——
	// 后者比睡死更糟,那是按拍烧 6 次 fork。
	for round := 0; round <= 70; round++ {
		got := nextReconcileInterval(round)
		if got < reconcileBaseInterval {
			t.Fatalf("nextReconcileInterval(%d)=%v < 基础周期 %v —— 溢出或回绕,会变成按拍烧",
				round, got, reconcileBaseInterval)
		}
		if got > reconcileMaxInterval {
			t.Fatalf("nextReconcileInterval(%d)=%v > 上限 %v —— 真出事时它已经睡死了",
				round, got, reconcileMaxInterval)
		}
	}
	// 退避必须单调不减:某一轮忽然变短,等于「越是长期没事、越可能突然按拍烧」。
	previous := first
	for round := 1; round <= 70; round++ {
		got := nextReconcileInterval(round)
		if got < previous {
			t.Fatalf("退避不得回缩: nextReconcileInterval(%d)=%v < 上一轮 %v", round, got, previous)
		}
		previous = got
	}
	// 负数是调用方算错了轮数,不是「立刻再来一轮」的指令。
	if got := nextReconcileInterval(-1); got < 30*time.Second {
		t.Fatalf("轮数为负时也不该短于基础周期, got %v", got)
	}
}

// 拿不到 mutation channel 就安静跳过 —— 绝不重试。
// channel 唤醒是 FIFO 的,硬重试的循环会把用户的 up 挤过 60s 预算变成 guardian_busy。
//
// 这条**刻意传 context.Background()**:调用方没有任何期限,所以「短 ctx」必须由
// reconcileOnce 自己加。若它直接拿调用方的 ctx 去 acquireMutation,这条测试会挂死
// 而不是失败 —— 那也是红,但读起来像基础设施坏了,所以下面还钉了一个时间上限。
func TestReconcileOnceYieldsSilentlyWhenMutationsAreBusy(t *testing.T) {
	env := newManagerTestEnv(t)
	// 模拟一次正在进行的用户变更:锁被别人占着,而且占的时间远长于循环该等的。
	if err := env.manager.acquireMutation(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer env.manager.releaseMutation()

	before := env.mutationCallCounts()
	done := make(chan reconcileDecision, 1)
	started := time.Now()
	go func() {
		done <- env.manager.reconcileOnce(context.Background(), observe.ObservedState{
			DNSManaged: observe.True,
		})
	}()

	var got reconcileDecision
	select {
	case got = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("拿不到 mutation channel 时必须安静跳过 —— 它却一直等着,这就是会把用户的 up 挤过 60s 预算的那种循环")
	}
	// **上限必须贴着「一次尝试」,不是「别挂死」。** 5 秒的宽限里塞得下 8 次
	// 200ms 的重试 —— 而「绝不重试」正是这条测试的全部内容:8 次重试就是 8 次
	// 插进 FIFO 队列,足以把用户那次 up 挤过 60s 预算。
	if elapsed := time.Since(started); elapsed > 3*reconcileMutationWait {
		t.Fatalf("放弃得太晚(超过一次尝试的量,像是在重试): %v", elapsed)
	}
	if got.Held != heldMutationBusy {
		t.Fatalf("跳过的理由必须说清是 mutation 忙 —— 否则日志里只剩「什么都没发生」, got %+v", got)
	}
	if len(got.Actions) != 0 {
		t.Fatalf("被栅栏挡住的一轮不许产出动作, got %v", got.Actions)
	}
	if after := env.mutationCallCounts(); after != before {
		t.Fatalf("跳过的一轮更不许动任何东西\nbefore=%+v\nafter =%+v", before, after)
	}
	// 锁自始至终在我们手上:reconcileOnce 从没抢到过,也没有在后台继续排队。
	select {
	case <-env.manager.mutation:
		t.Fatal("mutation channel 被 reconcileOnce 拿走了")
	default:
	}
}

// 路径恢复的栅栏升起时整轮跳过,而且**不去排 mutation 的队** ——
// 一轮注定跳过的观测没有理由挤在用户的 up 前面。
func TestReconcileOnceHoldsOnPathRecoveryFenceWithoutTakingTheMutationChannel(t *testing.T) {
	env := newManagerTestEnv(t)
	env.manager.beginPathRecoveryTransition(pathRecoveryTransitionPreserveGenerated)
	defer env.manager.endPathRecoveryTransition()

	// 把锁攥在手里:如果 reconcileOnce 仍去取它,下面就会等满超时才回来。
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
		if got.Held != heldPathRecoveryBusy {
			t.Fatalf("路径恢复在跑时必须报这道栅栏, got %+v", got)
		}
		if len(got.Actions) != 0 {
			t.Fatalf("被栅栏挡住的一轮不许产出动作, got %v", got.Actions)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("路径恢复栅栏升起时不该再去排 mutation 的队")
	}
}

func TestReconcileOnceHoldsWhenStartupRecoveryIsBlocked(t *testing.T) {
	env := newManagerTestEnv(t)
	env.manager.recoveryBlocked = true

	got := env.manager.reconcileOnce(context.Background(), observe.ObservedState{DNSManaged: observe.True})
	if got.Held != heldRecoveryBlocked {
		t.Fatalf("recoveryBlocked 为真时必须报这道栅栏, got %+v", got)
	}
	if len(got.Actions) != 0 {
		t.Fatalf("被栅栏挡住的一轮不许产出动作, got %v", got.Actions)
	}
}

// 所有权不确定是**锁存的 fail-closed 拒绝**,循环撞上它 = 本轮什么都不做,
// 而且**绝不由循环去清它**:清掉等于自动推翻一次刻意的拒绝,正是 af81632 被回退时
// 那个双 Core 风险的入口。
func TestReconcileOnceNeverClearsTheOwnershipUncertainLatch(t *testing.T) {
	env := newManagerTestEnv(t)
	env.manager.current = Process{PID: 4242, Uncertain: true}

	got := env.manager.reconcileOnce(context.Background(), observe.ObservedState{
		DNSManaged: observe.True,
		CoreSocket: observe.False,
	})
	if got.Held != heldOwnershipUncertain {
		t.Fatalf("所有权不确定时必须报这道栅栏, got %+v", got)
	}
	if !env.manager.current.Uncertain {
		t.Fatal("循环绝不能清掉所有权不确定的锁存 —— 那是一个存在意义就是「拒绝」的判断")
	}
}

// 日志只在差异**发生变化**时打一行。
//
// 五条常驻的「无法观测」把 divergence 训练成噪声,这是 cli 那道 darwin 门存在的
// 理由;循环若每拍一行,会把同一个错误重犯一遍,而且是每 30 秒一次。
//
// 顺带这条也是「整条循环跑完一个动作都没执行」的端到端断言 —— reconcileOnce
// 那条只覆盖单轮。
//
// **每一轮的 ObservedAt 都不一样,这是这条测试成立的前提。**
// 复审实测:所有假观测都用零值 ObservedAt 时,把 observed.ObservedAt 折进
// 「是否有变化」的比较里,这条测试**照样绿** —— 也就是它分不清「比的是判断」
// 与「比的是观测」,而后者在生产里意味着每 30 秒一行、永远。真实的 Observe
// 每轮都会盖一个新时间戳,假观测必须复刻这一点,否则守的是一个不存在的世界。
func TestReconcileLoopLogsOnlyWhenTheDecisionChanges(t *testing.T) {
	env := newManagerTestEnv(t)
	before := env.mutationCallCounts()
	logs := captureGuardianLog(t)

	diverged := observe.ObservedState{DNSManaged: observe.True}
	converged := observe.ObservedState{DNSManaged: observe.False}
	stamp := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	round := 0
	observer := func(context.Context) observe.ObservedState {
		// **观测期间绝不能有人持着 mutation channel**(Global Constraint:
		// 一整轮观测在 darwin 上是 6 次 fork 加约 900 次 syscall,攥着那把锁去做
		// 等于让用户的 up 排在 6 次 fork 后面)。这里现场求证一次:锁若取得到,
		// 就说明此刻没人持有;取完立刻还回去。
		select {
		case <-env.manager.mutation:
			env.manager.releaseMutation()
		default:
			t.Error("观测期间 mutation channel 被持有 —— 用户的 up 会排在整轮观测后面")
		}

		mu.Lock()
		defer mu.Unlock()
		round++
		// 每轮盖一个不同的时间戳,复刻真实 Observe 的行为(见函数头)。
		observedAt := stamp.Add(time.Duration(round) * time.Second)
		switch {
		case round <= 3:
			state := diverged // 第 1 轮打一行,第 2、3 轮必须静默
			state.ObservedAt = observedAt
			return state
		case round <= 5:
			state := converged // 第 4 轮变了,再打一行;第 5 轮静默
			state.ObservedAt = observedAt
			return state
		default:
			cancel()
			state := converged
			state.ObservedAt = observedAt
			return state
		}
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		env.manager.runReconcileLoopWithPacing(ctx, observer, func(int) time.Duration {
			return time.Millisecond
		})
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("循环没有随 ctx 结束")
	}

	lines := logLinesContaining(logs, "guardian_reconcile_would")
	if len(lines) != 2 {
		t.Fatalf("同一个判断只该打一行,变了才再打一行,got %d 行:\n%s", len(lines), strings.Join(lines, "\n"))
	}
	if !strings.Contains(lines[0], string(actionRestoreDNS)) {
		t.Errorf("第一行该说清本轮**本来会**做什么, got %q", lines[0])
	}
	if strings.Contains(lines[1], string(actionRestoreDNS)) {
		t.Errorf("差异消失那一行不该还挂着旧动作, got %q", lines[1])
	}
	if after := env.mutationCallCounts(); after != before {
		t.Fatalf("整条循环跑完,一个动作都不许执行\nbefore=%+v\nafter =%+v", before, after)
	}
}

// 循环必须随 ctx 结束,而且是在**睡觉的时候**也能被叫醒 —— 否则退避到 10 分钟
// 之后,一次关机要等它睡醒。
func TestReconcileLoopStopsWithItsContext(t *testing.T) {
	env := newManagerTestEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	observed := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		env.manager.runReconcileLoopWithPacing(ctx, func(context.Context) observe.ObservedState {
			select {
			case observed <- struct{}{}:
			default:
			}
			return observe.ObservedState{}
		}, func(int) time.Duration { return time.Hour })
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("循环在退避期间睡死了,ctx 取消也叫不醒")
	}
	select {
	case <-observed:
		t.Fatal("ctx 取消之后不该再观测一轮")
	default:
	}
}

// 循环那条 goroutine 里的 panic 必须被收住。
//
// 与 TestStartupRecoveryGoroutineSurvivesAPanic 同形、同理由:这里没有 net/http
// 那样的兜底,Guardian 一死 launchd 的 KeepAlive 就把它拉起来,再起循环再 panic ——
// 崩溃循环,而且用户机器上可能还留着屏障。
func TestReconcileLoopGoroutineSurvivesAPanic(t *testing.T) {
	d := &Daemon{}
	d.trackReconcileLoop(context.Background(), func(context.Context) { panic("调谐环里炸了") })

	select {
	case <-d.reconcileLoopDone:
	case <-time.After(2 * time.Second):
		t.Fatal("panic 之后那条 goroutine 应当正常收尾,而不是把进程带走")
	}
}

// **daemon 真的把循环接上了** —— 行为断言,不是读源码文本。
//
// 我在第一版报告里说这件事「只能等组装根变得可测」,那个结论是错的:
// startRecoveredDaemon 早就可测(已有五条测试往里注入 daemonStarter)。
// 而这件事**必须**有守卫,理由比一般的接线更硬:本阶段唯一真正的验收是真机
// soak,而验收方式是数日志里 guardian_reconcile_would 的行数,理想值是 **0**。
// 循环若悄悄没接上,产出的正是一份干净的日志 —— 一个漏装的循环与一台健康的机器
// 在这份证据上**完全无法区分**,而人会把它读成「零分歧,可以进③b 授权了」。
//
// 这条不跑任何外部命令:循环先睡一个基础周期(30s)再观测,而它在第一次睡眠里
// 就被 cancel 掉了。
func TestStartRecoveredDaemonWiresTheReconcileLoop(t *testing.T) {
	env := newManagerTestEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	daemon, err := startRecoveredDaemon(ctx, DaemonOptions{}, env.manager,
		func(context.Context, DaemonOptions) (*Daemon, error) { return &Daemon{}, nil })
	if err != nil {
		t.Fatal(err)
	}
	if daemon.reconcileLoopDone == nil {
		t.Fatal("daemon 没有起那条只观察的调谐环 —— 真机 soak 会拿到一份干净日志," +
			"而干净日志会被读成「零分歧」")
	}

	// 起来了还不够:它必须**在跑**,而且跟着 ctx 停。一条起了就立刻退出的
	// goroutine 同样会产出一份干净日志。
	cancel()
	select {
	case <-daemon.reconcileLoopDone:
	case <-time.After(5 * time.Second):
		t.Fatal("调谐环没有随 daemon 的 ctx 停下")
	}
	// 顺手收掉背景里的启动恢复,免得它跨测试还在跑。
	if err := daemon.waitForStartupRecovery(context.Background()); err != nil {
		t.Fatalf("等待启动恢复收尾: %v", err)
	}
}

// logLinesContaining 从抓下来的日志里挑出含某个记号的行。
//
// 只在被观察的 goroutine **已经结束之后**调用(测试里都是 <-done 之后),
// 所以直接读 captureGuardianLog 的 buffer 是安全的。
func logLinesContaining(buffer *bytes.Buffer, marker string) []string {
	var matched []string
	for _, line := range strings.Split(buffer.String(), "\n") {
		if strings.Contains(line, marker) {
			matched = append(matched, line)
		}
	}
	return matched
}
