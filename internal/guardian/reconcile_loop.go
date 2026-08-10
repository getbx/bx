package guardian

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/getbx/bx/internal/observe"
)

// 本文件是阶段③a 的调谐环:周期性地把「用户要什么」与「系统实际是什么」对一次,
// **把「我本来会做什么」记进日志,然后什么都不做。**
//
// 为什么要有一个什么都不做的循环:控制面至今没有调谐环,五条手写的补偿路径是它
// 的替代品。判据(reconcile.go 的 decide)是新写的,而它依赖的观测在真机上从没
// 连续跑过 —— `looksLikeCore` 的误报率至今**从未测量**。让它先跑一段只说不做的
// 日子,是唯一能在授权动手之前拿到误报率的办法。阶段③b 才逐项开授权。
//
// 三件事在这里是硬性的:
//
//   - **一个动作都不执行。** 本文件不碰任何 mutating hook(barrier/dns/runner/store)。
//     这条由行为断言守住(TestReconcileOnceExecutesNothing 与
//     TestReconcileLoopLogsOnlyWhenTheDecisionChanges 比对替身上的调用),
//     不是源码文本匹配 —— 本仓库的文本守卫上一轮被绕过了八次。
//   - **观测不许持有 mutation channel。** 一整轮观测在 darwin 上是 6 次进程 fork
//     (capture 2、barrier 1、DNS 3)加约 900 次 syscall。观测在循环里做完,
//     reconcileOnce 只拿着已经取到的那份事实去读栅栏 —— 它接一个
//     observe.ObservedState 参数而不是自己去观测,正是为此。
//   - **日志只在判断发生变化时打。** 每拍一行会把这个字段训练成噪声,而噪声会
//     训练人忽略它 —— cli/cli.go 那道「非 darwin 不附观测」的门就是同一个教训。
//     一台健康机器上这个循环应当**一行都不打**。

// reconcileObservation 是一次观测。注入而非直接调用 observe.Observe,
// 好让循环免 root 可测:真实实现要跑 route/networksetup 并连 Core 的控制 socket。
type reconcileObservation func(context.Context) observe.ObservedState

const (
	// reconcileBaseInterval 是最短周期。**不要往下调。** 一轮观测是 6 次 fork,
	// 而这个循环今天换不来任何动作,只换日志。
	reconcileBaseInterval = 30 * time.Second
	// reconcileMaxInterval 是退避上限:退避没有上限,真出事时它已经睡死了。
	reconcileMaxInterval = 10 * time.Minute
	// reconcileObserveTimeout 给一轮观测封顶,与 bx status 那边同值同理由:
	// 宁可少答一项(记 Unknown),也不能让一轮观测挂住整条循环。
	reconcileObserveTimeout = 5 * time.Second
	// reconcileMutationWait 是**取 mutation channel 的**上限,不是观测的。
	// 短:拿不到就跳过这一轮,30 秒后自然还有下一轮。
	reconcileMutationWait = 200 * time.Millisecond
)

// heldMutationBusy 是第四道栅栏的名字。
//
// 它不在 reconcileInput 里,是因为另外三道**读**得到,而这一道只能靠**试着取**
// 才能发现 —— 没有「窥视 channel 是否空闲」这种既准确又无副作用的读法。
const heldMutationBusy = "mutation_busy"

// nextReconcileInterval 按「连续多少轮判断没变」给出下一轮的间隔。
//
// 指数退避 + 上限。unchangedRounds 是**连续相同**的轮数,一旦判断变了就归零,
// 于是「刚出事的那一刻」永远是按基础周期在看,而「长期无事」几乎不花钱。
func nextReconcileInterval(unchangedRounds int) time.Duration {
	interval := reconcileBaseInterval
	for round := 0; round < unchangedRounds; round++ {
		interval *= 2
		if interval >= reconcileMaxInterval {
			return reconcileMaxInterval
		}
	}
	return interval
}

// reconcileOnce 判断一轮:读栅栏 → 组 reconcileInput → decide → 返回。
//
// **只读。** 它不执行 decide 提议的任何一项,也不改 Manager 的任何状态 ——
// 尤其不清 m.current.Uncertain:那是一个存在意义就是「拒绝」的 fail-closed 判断,
// 由循环去清等于自动推翻一次刻意的拒绝(af81632 那个双 Core 风险的入口)。
//
// observed 由调用方先取好:观测要 fork 进程,绝不能在 mutation channel 里做。
func (m *Manager) reconcileOnce(ctx context.Context, observed observe.ObservedState) reconcileDecision {
	input := reconcileInput{
		Desired:          m.Status().Desired,
		Observed:         observed,
		PathRecoveryBusy: m.pathRecoveryBusy(),
	}
	if input.PathRecoveryBusy {
		// 这道栅栏有自己的锁,不用抢 mutation channel 就读得到。先看它:
		// 一轮注定要跳过的观测没有任何理由排在用户的 up 前面。
		return decide(input)
	}
	// 另外两道只有拿到 mutation channel 才读得干净 —— 它们的写方都持着它
	// (upLocked/recoverLocked/handleUnexpectedExit)。
	//
	// **短 ctx,拿不到就安静跳过,绝不重试。** channel 的唤醒是 FIFO 的:一个
	// 硬重试的循环会不断把自己排进队里,把用户那次 `bx up` 挤过 60s 预算,
	// 变成一个它完全无从理解的 guardian_busy。跳过没有代价 —— 下一轮就在
	// 30 秒后,而本期反正一个动作都不会执行。
	blocked, uncertain, acquired := m.readMutationFences(ctx)
	if !acquired {
		return reconcileDecision{Held: heldMutationBusy}
	}
	input.RecoveryBlocked = blocked
	input.OwnershipUncertain = uncertain
	return decide(input)
}

// readMutationFences 取一次 mutation channel,读出躲在它后面的两道栅栏,立刻还回去。
//
// 用 defer 还锁而不是读完手动还:这中间任何一次 panic 若把 channel 漏掉,
// Guardian 的每一次 up/down 都会永久卡在 acquireMutation 上 —— 一个只观察的
// 循环把整个控制面锁死,是这一期能造成的最坏后果。
func (m *Manager) readMutationFences(ctx context.Context) (recoveryBlocked, ownershipUncertain, acquired bool) {
	acquireCtx, cancel := context.WithTimeout(ctx, reconcileMutationWait)
	defer cancel()
	if err := m.acquireMutation(acquireCtx); err != nil {
		return false, false, false
	}
	defer m.releaseMutation()
	return m.recoveryBlocked, m.current.Uncertain, true
}

// pathRecoveryBusy 报告路径恢复的栅栏是否升起(正在恢复,或有变更正把它按住)。
func (m *Manager) pathRecoveryBusy() bool {
	m.pathRecoveryMu.Lock()
	defer m.pathRecoveryMu.Unlock()
	return m.pathRecoveryFences > 0 || m.pathRecoveryActive
}

// runReconcileLoop 是生产入口:按 nextReconcileInterval 的节奏跑,直到 ctx 结束。
func (m *Manager) runReconcileLoop(ctx context.Context, observer reconcileObservation) {
	m.runReconcileLoopWithPacing(ctx, observer, nextReconcileInterval)
}

// runReconcileLoopWithPacing 把节奏做成参数,好让循环本身可以在单测里跑完整几轮
// 而不必真的睡 30 秒。生产只有 runReconcileLoop 一个调用方。
//
// **先睡后观测**:Guardian 刚起来时启动恢复正在跑,第一轮判断没有意义。
func (m *Manager) runReconcileLoopWithPacing(ctx context.Context, observer reconcileObservation, pacing func(int) time.Duration) {
	// previous 的初值是「无动作、无栅栏」,也就是一台健康机器的判断。于是健康
	// 机器上这个循环一行日志都不打 —— 真机 soak 要数的正是这个 0。
	var previous reconcileDecision
	unchanged := 0
	for {
		if !waitReconcileInterval(ctx, pacing(unchanged)) {
			return
		}
		observeCtx, cancelObserve := context.WithTimeout(ctx, reconcileObserveTimeout)
		observed := observer(observeCtx)
		cancelObserve()
		if ctx.Err() != nil {
			// 观测途中开始关机了。**不判断、不打日志**:此刻 acquireMutation
			// 一定失败,而那会被记成 held=mutation_busy —— 一句假话,并且是
			// 每次关机都出现的一句假话,正好把这行日志训练成噪声。
			return
		}

		decision := m.reconcileOnce(ctx, observed)
		if sameReconcileDecision(previous, decision) {
			unchanged++
			continue
		}
		previous = decision
		unchanged = 0
		log.Printf("guardian_reconcile_would actions=%s held=%s observed=%s",
			formatReconcileActions(decision.Actions), formatHeld(decision.Held), formatObserved(observed))
	}
}

// waitReconcileInterval 睡一个周期,ctx 结束则立刻返回 false。
// 睡的时候也必须叫得醒:退避到 10 分钟之后,一次关机不该等它睡满。
func waitReconcileInterval(ctx context.Context, interval time.Duration) bool {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// sameReconcileDecision 判断两轮判断是不是同一件事。
//
// **刻意不比 ObservedState。** 那里面有 ObservedAt,每轮都在变;把它算进去等于
// 每拍一行日志,正是这条约束要消灭的噪声。判断相同就意味着「本来会做的事」相同,
// 而那才是这行日志说的内容。
func sameReconcileDecision(a, b reconcileDecision) bool {
	if a.Held != b.Held || len(a.Actions) != len(b.Actions) {
		return false
	}
	for index := range a.Actions {
		if a.Actions[index] != b.Actions[index] {
			return false
		}
	}
	return true
}

func formatReconcileActions(actions []reconcileAction) string {
	if len(actions) == 0 {
		return "none"
	}
	names := make([]string, 0, len(actions))
	for _, action := range actions {
		names = append(names, string(action))
	}
	return strings.Join(names, ",")
}

func formatHeld(held string) string {
	if held == "" {
		return "none"
	}
	return held
}

// formatObserved 把这一轮的事实压成一行。三态原样透出(true/false/unknown):
// 把 unknown 印成 false 就是把「没问出来」写成「问过、没有」,而整个
// internal/observe 就是为了消灭这种谎言存在的。
func formatObserved(observed observe.ObservedState) string {
	line := "capture=" + observed.CaptureOK.String() +
		" barrier=" + observed.BarrierPresent.String() +
		" dns=" + observed.DNSManaged.String() +
		" core_socket=" + observed.CoreSocket.String() +
		" tunnel=" + observed.TunnelHealthy.String()
	if len(observed.Errors) == 0 {
		return line
	}
	items := make([]string, 0, len(observed.Errors))
	for _, failure := range observed.Errors {
		items = append(items, failure.Item)
	}
	return line + " unobservable=" + strings.Join(items, ",")
}
