package guardian

import (
	"context"
	"log"
	"runtime/debug"
	"slices"
	"strconv"
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

// ReconcileStaleAfter 是「这份报告已经不新鲜了」的判据,给消费方(CLI/菜单)用。
//
// 取退避上限的两倍:上限本身是合法的轮距,一份刚好隔了一个上限的报告完全正常;
// 两倍则意味着**至少整整一轮没发生过** —— 循环死了、或者每一轮都在 panic
// (panic 的那一轮刻意不写报告,见 runReconcileRound)。
//
// 导出它是因为判据只能有一个:CLI 自己抄一个 20 分钟的常量,就是把「循环多久
// 跑一次」这件只有 Guardian 知道的事复制到了另一个进程里,而那两个数字迟早分叉。
const ReconcileStaleAfter = 2 * reconcileMaxInterval

// heldMutationBusy 是唯一一道不住在 reconcileInput 里的栅栏。
//
// 别的几道(recovery_blocked / path_recovery / ownership / maintenance_hold /
// intent_unreadable)都**读**得到,而这一道只能靠**试着取**才能发现 ——
// 没有「窥视 channel 是否空闲」这种既准确又无副作用的读法。
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
		Observed:         observed,
		PathRecoveryBusy: m.pathRecoveryBusy(),
	}
	// **从磁盘读,不读 m.Status().Desired。** 内存里的 Desired 会被 needsAttention
	// 写成调用方传进来的常量(manager.go:1470-1473),好几处传的是字面量 DesiredOn
	// 而磁盘写着 off;而挂起只在磁盘上。两者必须是同一次读出来的 —— 一半读内存
	// 一半读磁盘,一轮之内就能出现两者互不相干的组合(设计取舍六)。
	intent, err := m.loadIntentSnapshot(time.Now())
	if err != nil {
		// **不许塌缩成 off 或 on。** 读不出来是没有答案,不是某个答案:
		// 按 off 收敛会去停一个用户要的 Core,按 on 收敛会在维护窗口里起一个
		// 不该起的。Desired 保持零值 —— 反正栅栏在 decide 的第一句就短路了。
		input.IntentUnreadable = true
	} else {
		input.Desired = intent.Desired
		input.MaintenanceHold = intent.HoldArmed
	}
	if input.PathRecoveryBusy || input.MaintenanceHold || input.IntentUnreadable {
		// 这几道栅栏不用抢 mutation channel 就读得到(路径恢复有自己的锁,
		// 另两道来自刚刚那次读盘)。先看它们:一轮注定要跳过的判断没有任何
		// 理由排在用户的 up 前面。
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

// reconcileRound 是一轮的完整产物:判断、这一轮**没能看见**什么、以及那次只读
// 的进程扫描测到了什么。
//
// 判断单独比较是不够的:三项探测全失败时判断恰好等于一台健康机器的判断
// (Unknown 一律「什么都不做」)。「变没变」必须连观测质量一起比,否则一台
// 从此永久失明的机器从失明那一刻起一行日志都不会再打。
type reconcileRound struct {
	decision     reconcileDecision
	unobservable []string
	scan         ReconcileCoreScan
	unchanged    int
}

// runReconcileLoopWithPacing 把节奏做成参数,好让循环本身可以在单测里跑完整几轮
// 而不必真的睡 30 秒。生产只有 runReconcileLoop 一个调用方。
//
// **先睡后观测**:Guardian 刚起来时启动恢复正在跑,第一轮判断没有意义。
//
// **panic 收在每一轮里,不在整条循环外面。** 上一版把 recover 放在 goroutine 那
// 一层(daemon.go 的 trackReconcileLoop),于是第一次 panic 就是循环的终点:
// reconcileLoopDone 关掉、没人重启、而 Status().Reconcile 冻在最后一轮上,
// `bx status` 把它渲染成一轮干净的观测 —— 一条死掉的循环读起来完全正常。
func (m *Manager) runReconcileLoopWithPacing(ctx context.Context, observer reconcileObservation, pacing func(int) time.Duration) {
	// previous 的初值是「无动作、无栅栏、什么都观测得到、还没测过 Core 进程数」,
	// 也就是一台健康机器**跑起来之后**的第一轮会与之比较的基准。健康机器上这个
	// 循环因此至多在第一轮打一行(记下那次扫描的基线),此后静默 —— 真机 soak
	// 要数的正是随后那个 0。而一台起手就失明的机器会立刻打一行,不再是静默。
	var previous reconcileRound
	panics := 0
	for {
		interval := pacing(previous.unchanged)
		if panics > 0 {
			interval = reconcilePanicBackoff(interval, panics)
		}
		if !waitReconcileInterval(ctx, interval) {
			return
		}
		round, stop, panicked := m.runReconcileRound(ctx, observer, previous, panics)
		switch {
		case panicked:
			// **不记报告。** 一轮炸掉的轮次没有产出判断,发布一份就是编造;而
			// 让 At 停在上一轮,正是消费方判定「这份报告已停滞」的依据
			// (ReconcileStaleAfter)。一条永久 panic 的循环因此在 20 分钟内
			// 会在 bx status 上现形,而不是安静地重试到天荒地老。
			panics++
		case stop:
			return
		default:
			panics = 0
			previous = round
		}
	}
}

// runReconcileRound 跑一轮:观测 → 只读测量 → 判断 → 记报告 → 变了才打日志。
//
// 三个返回值:这一轮的产物、要不要结束整条循环、以及这一轮是不是炸了。
// panic 时 stop 必为 false —— 一轮炸掉不是停下来的理由,那正是本次修复的全部内容。
func (m *Manager) runReconcileRound(ctx context.Context, observer reconcileObservation, previous reconcileRound, consecutivePanics int) (round reconcileRound, stop, panicked bool) {
	defer func() {
		if r := recover(); r != nil {
			round, stop, panicked = reconcileRound{}, false, true
			log.Printf("guardian_reconcile_round_panicked consecutive=%d recovered=%v\n%s",
				consecutivePanics+1, r, debug.Stack())
		}
	}()

	observeCtx, cancelObserve := context.WithTimeout(ctx, reconcileObserveTimeout)
	observed := observer(observeCtx)
	cancelObserve()
	if ctx.Err() != nil {
		// 观测途中开始关机了。**不判断、不打日志**:此刻 acquireMutation
		// 一定失败,而那会被记成 held=mutation_busy —— 一句假话,并且是
		// 每次关机都出现的一句假话,正好把这行日志训练成噪声。
		return reconcileRound{}, true, false
	}

	// 只读测量,排在取 mutation channel **之前**:它是约 900 次 syscall,
	// 攥着那把锁去做等于让用户的 up 排在后面(与观测同一条纪律)。
	round = reconcileRound{
		unobservable: observed.UnobservableItems(),
		scan:         m.measureRunningCores(),
	}
	round.decision = m.reconcileOnce(ctx, observed)
	changed := !sameReconcileRound(previous, round)
	if changed {
		round.unchanged = 0
	} else {
		round.unchanged = previous.unchanged + 1
	}
	// **每一轮都记,包括没变的那些、以及被栅栏挡住的那些。**
	//
	// 日志按「变了才打」是为了不制造噪声,而报告是**状态**不是事件:一份
	// 只在有差异时才更新的报告,在一台健康机器上会永远停在 nil,而 nil 的
	// 意思是「循环从没跑过一轮」—— 这正好把本任务要消灭的那个歧义原样搬进
	// 了 bx status。
	m.recordReconcileRound(round)
	if !changed {
		return round, false, false
	}
	log.Printf("guardian_reconcile_would actions=%s held=%s core_scan=%s observed=%s",
		formatReconcileActions(round.decision.Actions), formatHeld(round.decision.Held),
		formatCoreScan(round.scan), formatObserved(observed))
	return round, false, false
}

// reconcilePanicBackoff 在连续 panic 时把间隔翻倍,封顶同退避上限。
//
// 没有它,一个**确定性**会 panic 的轮次(某个解析函数撞上一种输出格式)就是一台
// 满速空转的机器:每一轮都 fork 一遍观测、炸一遍、立刻再来。有它则最坏每 10 分钟
// 一次,而报告停滞会在 bx status 上说明发生了什么。
//
// 与 nextReconcileInterval 同形(逐次翻倍 + 命中上限即返回),所以同样不会溢出:
// time.Duration 是 int64,不设这个早退的实现会在第 54 次翻倍时绕回 0。
//
// **绝不返回比 base 更短的间隔**:pacing 已经退避到上限之后再撞上 panic,
// 「翻倍」若把它拉回上限就成了加速,方向正好反了。
func reconcilePanicBackoff(base time.Duration, consecutivePanics int) time.Duration {
	interval := base
	for round := 0; round < consecutivePanics; round++ {
		if interval >= reconcileMaxInterval {
			return interval
		}
		interval *= 2
		if interval >= reconcileMaxInterval {
			return reconcileMaxInterval
		}
	}
	return interval
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

// sameReconcileRound 判断两轮是不是同一回事:判断、观测质量、Core 进程测量,
// 三样都相同才算没变。
//
// **刻意不比 ObservedState 整体。** 那里面有 ObservedAt,每轮都在变;把它算进去
// 等于每拍一行日志,正是这条约束要消灭的噪声。
//
// **但观测质量必须算进来。** 判断相同不代表这两轮是同一回事:三项探测全失败时
// decide 对每个 Unknown 都「什么都不做」,产出的判断与一台完全健康的机器逐字节
// 相同。只比判断的那一版,会让一台从此永久失明的机器从失明那一刻起再也不打一行
// 日志,而 bx status 上写着「无差异(连续 N 轮未变)」——「我们从没问过」与
// 「问了、一切正常」渲染成同一句话,这个分支上已经犯过三次了。
//
// Core 进程测量同理:它是本阶段的第二样交付(looksLikeCore 的误报率),
// 1 变成 2 正是要抓的那个事件,而不是要压掉的噪声。
func sameReconcileRound(a, b reconcileRound) bool {
	return sameReconcileDecision(a.decision, b.decision) &&
		a.scan == b.scan &&
		slices.Equal(a.unobservable, b.unobservable)
}

// sameReconcileDecision 只比「本来会做的事」这一半:动作序列与栅栏。
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

// formatCoreScan 把那次只读测量压成一段。**「没测成」与「测到 0 个」必须写得
// 不一样**:把扫描失败印成 cores=0 就是把「问不出来」写成「问过、没有」。
func formatCoreScan(scan ReconcileCoreScan) string {
	if !scan.Measured {
		reason := scan.Reason
		if reason == "" {
			reason = "unknown"
		}
		return "unmeasured:" + reason
	}
	return strconv.Itoa(scan.Cores)
}

// formatObserved 把这一轮的事实压成一行。三态原样透出(true/false/unknown):
// 把 unknown 印成 false 就是把「没问出来」写成「问过、没有」,而整个
// internal/observe 就是为了消灭这种谎言存在的。
//
// unobservable 取 observe 自己的那份清单(UnobservableItems),不再从 Errors 推:
// 依赖缺席时一项同样问不出来却一条错误都不留,按 Errors 推会把它印成「全都问到了」。
func formatObserved(observed observe.ObservedState) string {
	line := "capture=" + observed.CaptureOK.String() +
		" barrier=" + observed.BarrierPresent.String() +
		" dns=" + observed.DNSManaged.String() +
		" core_socket=" + observed.CoreSocket.String() +
		" tunnel=" + observed.TunnelHealthy.String()
	items := observed.UnobservableItems()
	if len(items) == 0 {
		return line
	}
	return line + " unobservable=" + strings.Join(items, ",")
}
