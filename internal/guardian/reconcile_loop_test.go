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
	if got := nextReconcileInterval(1000); got > 10*time.Minute {
		t.Errorf("退避要有上限,否则真出事时它已经睡死了, got %v", got)
	}
	// 退避必须单调不减:某一轮忽然变短,等于「越是长期没事、越可能突然按拍烧」。
	previous := first
	for round := 1; round <= 20; round++ {
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
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("放弃得太晚: %v", elapsed)
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
func TestReconcileLoopLogsOnlyWhenTheDecisionChanges(t *testing.T) {
	env := newManagerTestEnv(t)
	before := env.mutationCallCounts()
	logs := captureGuardianLog(t)

	diverged := observe.ObservedState{DNSManaged: observe.True}
	converged := observe.ObservedState{DNSManaged: observe.False}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	round := 0
	observer := func(context.Context) observe.ObservedState {
		mu.Lock()
		defer mu.Unlock()
		round++
		switch {
		case round <= 3:
			return diverged // 第 1 轮打一行,第 2、3 轮必须静默
		case round <= 5:
			return converged // 第 4 轮变了,再打一行;第 5 轮静默
		default:
			cancel()
			return converged
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
