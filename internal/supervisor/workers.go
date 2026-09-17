package supervisor

import (
	"context"
	"log"
	"runtime/debug"
	"sync"
)

// workerRegistry 是 Run 的后台工人登记册:Run 里那些长命 goroutine 从裸
// `go f(ctx)` 改为经它启动。当下的全集重数一遍就有(2026-09-13 是八个):
//
//	grep -n 'workers.start(' internal/supervisor/run.go
//
// **动它的理由是一条硬后果,不是整洁**:裸 goroutine 里的 panic 不会被 Run 的
// defer 接住 —— Go 在那种情况下当场终止进程,**别的 goroutine 的 defer 一个都
// 不会跑**。对 bx 这意味着进程没了而内核里的 ip rule / 策略路由**还在**:整机
// 流量指向一个已经不存在的 TUN,也就是断网,而且是那种「重启 bx 才能修」的断网。
//
// **为什么一律 recover-and-continue**,逐个想过而不是图省事。这份枚举答的是
// 「这一个死了会静默降级成什么」,所以**加了工人就要在这里加一条**:
//   - mutation-engine(commit-confirmed 引擎)死 ⇒ `bx server use` 不工作,网络照常;
//   - tailscale-bypass 死 ⇒ 旁路停在兜底表,网络照常;
//   - transport-failover 死 ⇒ 不再自动切备,而 kill-switch 仍在(fail-closed 不受影响);
//   - direct-egress-repair 死 ⇒ 直连出口不再自愈(2026-08-13 那个 bug 会回来);
//   - rule-history 死 ⇒ 规则历史停止累计;
//   - server-bypass-refollow 死(仅 darwin)⇒ 服务器换了 IP 之后不再跟随,
//     隧道永远连旧地址(2026-09-04 记的那次:NAS 上重连了 65638 次、一个月才被发现);
//   - server-bypass-route-repair 死(仅 darwin)⇒ 休眠唤醒后被冲掉的 /32 旁路
//     不再自愈,隧道成环(2026-09-04 那次 13 分钟断网);
//   - china-list-refresh 死 ⇒ 列表不再更新。
//
// **后两个是 2026-09-04 加的,而这份枚举当时没跟上,直到 2026-09-13 的审计才补。**
//
// 这几件里没有一件值得用「一台受保护的机器断网」来换。**但代价要说清楚**:炸掉的
// 工人就此不再跑,那是一次**静默降级**。它打一行带栈的日志,并把名字记进
// panicked。
//
// **别把那份记账读成「答得出少了哪个后台循环」** —— 这段话原先是这么写的,
// 而 names()/panickedNames() 至今**零生产调用方**(只有 workers_test.go 读它们):
// 它们既不进 `bx status`、也不进控制 socket,今天唯一的办法仍然是翻日志。
// 留着不删是因为把「哪个工人炸了」发布出去是一次真实的改动,而不是因为已经做了;
// 要做的时候记得那条老判断:一个没有阈值的常驻数字会变墙纸。
type workerRegistry struct {
	mu       sync.Mutex
	started  []string
	panicked []string
	// onPanic 是测试钩子。生产里为 nil —— 记账与日志都在下面无条件做,
	// 钩子只是让测试不必去解析日志。
	onPanic func(name string, recovered any)
}

// start 起一个具名工人。fn 正常返回或随 ctx 退出都算干净结束,不记为故障 ——
// 把「关机时每个工人都退出了」记成一份死亡名单,会让真正的故障淹在里面。
func (r *workerRegistry) start(ctx context.Context, name string, fn func(context.Context)) {
	r.mu.Lock()
	r.started = append(r.started, name)
	r.mu.Unlock()

	go func() {
		defer func() {
			recovered := recover()
			if recovered == nil {
				return
			}
			r.mu.Lock()
			r.panicked = append(r.panicked, name)
			onPanic := r.onPanic
			r.mu.Unlock()
			// 大声,并且带栈 —— 一个静默降级的后台循环是这个仓库最典型的
			// 失效形状:它与「一切正常」在输出上完全一样。
			log.Printf("the background worker %q panicked and was contained (that worker stops here; the rest of bx keeps running): %v\n%s",
				name, recovered, debug.Stack())
			if onPanic != nil {
				onPanic(name, recovered)
			}
		}()
		fn(ctx)
	}()
}

// names 报出启动过哪些工人(按启动顺序)。
func (r *workerRegistry) names() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.started...)
}

// panickedNames 报出哪些工人炸掉了、就此不再跑。
func (r *workerRegistry) panickedNames() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.panicked...)
}
