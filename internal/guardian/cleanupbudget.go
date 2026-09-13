package guardian

import (
	"time"

	"github.com/getbx/bx/internal/supervisor"
)

// 关掉一个**服务过**的 Core 是一条三层嵌套的等待,而三层此前是三个各写各的 15s:
//
//	Manager.cleanupTimeout ─包住─→ ExecCoreRunner.StopTimeout ─等的是─→ Core 自己的
//	                                                                    supervisor.ShutdownGrace
//
// 三个数完全相等 = 零余量。Core 的关机 watchdog 是「跑满 grace 才强制退出」,
// 也就是说一个**把 defer 还原跑到最后一刻**的健康 Core(还原默认路由、关 TUN、
// 把 DNS 交回系统 —— 那正是我们让它协作关闭的全部理由)会恰好踩在这两个预算上:
// Stop 返回 deadline exceeded ⇒ retainUncertain ⇒ **core_ownership_uncertain**,
// 一台关得干干净净的机器被报成「系统里可能有第二个 Core」。
//
// 本仓库的先例是 TestTeardownStepBudgetLeavesRoomBeforeTheShutdownWatchdog:
// **等别人的那个预算必须给它留余量**。这里同一条:
//
//	defaultCoreCleanupTimeout > defaultCoreStopWait > supervisor.ShutdownGrace
//
// 由 TestCoreCleanupBudgetLeavesRoomAboveWhatItWaitsOn 钉住 —— 钉的是这个**顺序**,
// 不是任何一个具体数字。预算是天花板不是开销:正常关机实测 <1s,把上限抬高不会
// 让任何一次正常的 down 变慢一毫秒。
const (
	// defaultCoreStopWait:runner.Stop 请求协作关闭之后,等那个进程真的消失多久。
	// 它等的是 Core 自己的 defer 链,上界就是 ShutdownGrace(超过它 Core 会被
	// 自己的 watchdog 强制退出)—— 所以只要排在 ShutdownGrace 之后,一次「等超时」
	// 就真的意味着出了别的事,而不是「它正在好好地还原」。
	defaultCoreStopWait = supervisor.ShutdownGrace + 5*time.Second
	// defaultCoreCleanupTimeout:Manager 给整条清理(含发出那次关闭请求)的预算,
	// 必须再包得住 defaultCoreStopWait。
	defaultCoreCleanupTimeout = defaultCoreStopWait + 5*time.Second
)
