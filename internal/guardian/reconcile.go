package guardian

import "github.com/getbx/bx/internal/observe"

// 本文件只做**判断**,不做任何事。
//
// 控制面至今没有调谐环,五条手写的补偿路径就是它的替代品;每加一种新的分叉方式
// 就得再手写一条。这里抽出的是那半个「判断」:把用户想要什么(Desired)与系统
// 现在看起来是什么样(observe.ObservedState)一对,产出一组**命名的意图**。
// 本期(阶段③a)不执行其中任何一项,授权要等阶段③b 逐项开。
//
// 三条设计取舍,写在这里免得下一个读者顺手拆掉:
//
// 一、**动作是「命名的意图」,不是函数指针。**
//     reconcileAction 是字符串常量而不是 func() error,有三个理由:
//     (a) 日志能读懂 —— 「本轮提议 restore_dns」是人能核对的一句话,而一个闭包
//         在日志里只是个地址;
//     (b) 阶段③b 要**一项一项**地授权执行,白名单按名字写才审得动,若判据直接
//         递出可调用的函数,「决定」与「授权」之间就没有缝可以插检查;
//     (c) decide 因此是纯函数,没有 I/O、没有并发、也没有平台依赖 —— Guardian
//         只在 darwin 上跑、本项目的集成 harness 又只有 Linux,判据是整个调谐器
//         里唯一一处哪儿都能测的地方。同样的理由此前已经把 decideCoreScan
//         (procscan.go)和 failoverPolicy.decide(internal/supervisor)抽成过
//         纯函数。
//
// 二、**没有「装屏障」这个动作,是刻意的,不是漏了。**
//     屏障是四条 IPv4 + 四条 IPv6 的 /2 reject 路由,比 Core 的 /1 split-default
//     更长,按最长前缀匹配压过一切。装它要先探默认网关;探测**瞬时**失败时安装
//     会降级成 block-only —— 没有任何 server bypass,即整机黑洞,连隧道自己都没
//     有恢复的通路。这个系统里别的失误用户都还能自救,唯独一条被留下的 /2 reject
//     让用户连修复包都下不了。手写路径里它至少还绑在一次用户显式请求上;放进一个
//     按时钟驱动的循环,就是每一拍重掷一次这个骰子。故判据只提议**删**孤儿屏障,
//     永远不提议装。TestDecideNeverProposesInstallingABarrier 盯着这一条。
//
// 三、**所有权不确定(OwnershipUncertain)是栅栏,不是待收敛的差异。**
//     它是 Guardian 的 fail-closed 拒绝:扫描到一个可能是 Core 的进程、或者压根
//     问不出来,于是拒绝再起一个 Core —— 它整个存在的意义就是说「不」。调谐器若
//     把它当成一处「事实与意图不符」去消除,等于自动地去推翻一次刻意的拒绝,正是
//     af81632 被回退时那个双 Core 风险的入口。它和 recoveryBlocked、正在进行的
//     路径恢复一样:本轮什么都不做,并说清是被哪道栅栏挡住的。

// reconcileAction 是一项**被提议**的动作的名字。本期没有任何一项会被执行。
type reconcileAction string

const (
	actionNone               reconcileAction = ""
	actionRestoreDNS         reconcileAction = "restore_dns"
	actionClearOrphanBarrier reconcileAction = "clear_orphan_barrier"
	actionStartCore          reconcileAction = "start_core"
	actionStopCore           reconcileAction = "stop_core"
)

// reconcileInput 是一轮判断的全部输入。除此之外 decide 不看任何东西 —— 不读盘、
// 不发命令、不看时钟。
type reconcileInput struct {
	Desired  DesiredState
	Observed observe.ObservedState
	// 栅栏:任一为真即本轮什么都不做。分开列是为了日志能说清是哪一道。
	RecoveryBlocked    bool
	PathRecoveryBusy   bool
	OwnershipUncertain bool
}

// reconcileDecision 是一轮判断的产物。
type reconcileDecision struct {
	Actions []reconcileAction
	// Held 非空时 Actions 必为空,内容是「因为哪道栅栏」。
	Held string
}

// 栅栏的名字。它们会原样进日志,所以是稳定的标识符而不是给人看的句子。
const (
	heldRecoveryBlocked    = "recovery_blocked"
	heldPathRecoveryBusy   = "path_recovery_in_flight"
	heldOwnershipUncertain = "ownership_uncertain"
)

// decide 把意图与事实对一次,产出本轮提议做的事。
//
// 两条铁律:
//   - 栅栏在**最前面**判。任何一道升起,整轮停摆并说明理由 —— 不是「照常收敛」。
//   - 其后每一条规则都必须确认对应的观测**恰好**是 True 或 False。
//     observe.Tristate 的零值是 Unknown,而 Unknown 的意思是「没问出来」,不是
//     「观测到没有」。按 Unknown 收敛,就等于一次 route 命令超时,调谐器便去
//     「修」一个它根本没看见的问题 —— internal/observe 整个包就是为了消灭这种
//     谎言而存在的,判据这一侧不能把它又换个地方犯一遍。
func decide(in reconcileInput) reconcileDecision {
	if held := heldBy(in); held != "" {
		return reconcileDecision{Held: held}
	}

	var actions []reconcileAction
	add := func(a reconcileAction) { actions = append(actions, a) }

	switch in.Desired {
	case DesiredOn:
		// 观测到 Core 的控制 socket 确实不应答,才提议起它。
		// 本期只是**提议**:真授权要等 Uncertain 锁存那一段有解(见设计),
		// 否则一次瞬时的扫描失败会经由锁存变成永久拒绝。
		if in.Observed.CoreSocket == observe.False {
			add(actionStartCore)
		}
		// 注意此处没有、也不会有任何一条依据 CaptureOK / BarrierPresent 去
		// 「补装」保护的规则:见文件头第二条。劫持没生效是 Core 自己的事,
		// 调谐器不越过 Core 去动路由。

	case DesiredOff:
		// 用户要关,而这三样还挂在系统上,就是残留。顺序按「先停源头、再拆它
		// 留下的东西」排,只影响日志可读性 —— 本期一项都不会被执行。
		if in.Observed.CoreSocket == observe.True {
			add(actionStopCore)
		}
		if in.Observed.BarrierPresent == observe.True {
			add(actionClearOrphanBarrier)
		}
		if in.Observed.DNSManaged == observe.True {
			add(actionRestoreDNS)
		}
	}

	return reconcileDecision{Actions: actions}
}

// heldBy 返回挡住本轮的那道栅栏的名字,没有则返回空串。
//
// 三道分开判、按固定次序报第一道:同时升起时日志里出现哪一个必须是确定的,
// 否则同一个故障每次读到的原因都不一样。
func heldBy(in reconcileInput) string {
	switch {
	case in.RecoveryBlocked:
		return heldRecoveryBlocked
	case in.PathRecoveryBusy:
		return heldPathRecoveryBusy
	case in.OwnershipUncertain:
		return heldOwnershipUncertain
	default:
		return ""
	}
}
