package supervisor

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/getbx/bx/internal/confirm"
)

// 路由就绪位自愈(known-gaps B2,2026-09-23)。
//
// `RuntimeState.RoutesInstalled` 是一份**记账**:启动时 Hijack 成功置真、
// Rehijack 的 apply 成功置真、apply/undo 置假。一次**拆到一半**才失败的 rehijack
// 把它清成 false 是对的(路由真的可能坏了),但此后**没有任何东西会把它设回来**:
// 路径恢复的 verify 验不过、Guardian 的 health 门拦住升级、
// recoverySupersededByCore 不认账 —— 直到 Core 重启。
//
// **修法刻意不是「去内核看一眼,看着没问题就置真」**:那是凭观测放行,观测
// 覆盖不到的那一条路由(v6 reject、scoped 默认路由、某条 /32)会被一起宣布
// 完好,方向是放宽 fail-closed。这里做的是**重新完整装一遍**,就绪位只在一次
// 真的成功安装之后才回到 true —— 判据一个字都没放松,与 bypass_route_repair
// 走的是同一个入口(Rehijack 的 apply,在控制面那把锁里)。

// errRoutesAlreadyReady:拿到锁之后发现就绪位已经是真的(比如一次换服务器刚好
// 在这期间成功了)。**不许报成「重装成功」** —— 这一轮什么都没做。
var errRoutesAlreadyReady = errors.New("the capture routes are already marked installed; nothing to do")

// errRouteRepairDeferred:有一次待确认的改动,它自己的 apply 里带着一次 rehijack,
// 交错就是两份路由计划互相拆台。让路要说出来,不许报成「重装成功」。
var errRouteRepairDeferred = errors.New("a server change is waiting for confirmation; not touching the routes until it settles")

// routesReadyCheckInterval 与 direct_egress 同节拍;失败之后的重试由
// watchKernelRoute 的冷静期限频(egressRepairCooldown)。
const routesReadyCheckInterval = egressCheckInterval

// reinstallRoutesIfNotReady 在控制面的锁里、**就绪位仍为假时**重新落实全部路由。
//
// 就绪位在锁里再查一遍:决定修与拿到锁之间,一次换服务器可能刚好成功了,那时
// 再装一遍只是一次白白的路由抖动。
func (cs *controlServer) reinstallRoutesIfNotReady(_ context.Context, ready func() bool) error {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if ready() {
		return errRoutesAlreadyReady
	}
	if cs.eng.State() == confirm.StateArmed {
		return errRouteRepairDeferred
	}
	apply, _, err := cs.mut.Rehijack()
	if err != nil {
		return fmt.Errorf("preparing to reinstall the routes: %w", err)
	}
	if err := apply(); err != nil {
		return fmt.Errorf("reinstalling the routes: %w", err)
	}
	if !ready() {
		// apply 报了成功而就绪位没回来:不是我们看得见的那种成功,不许宣称修好了。
		return errors.New("the reinstall reported success but the routes are still not marked installed")
	}
	return nil
}

// watchRoutesReady 是那条循环。「坏了」的判据就是就绪位本身 —— 它是一份记账,
// 这里不另造一个观测去替它作答(见文件头)。循环体与 direct_egress /
// server_bypass 共用 watchKernelRoute:同一个冷静期、同一套 change-only 日志。
func watchRoutesReady(ctx context.Context, ready func() bool, repair egressRepair, tick <-chan time.Time) {
	watchKernelRoute(ctx, routeWatchMessages{
		recovered:    "routes_ready recovered: the capture routes are marked installed again",
		broken:       "routes_ready is false while bx is running: a route change most likely failed halfway, and nothing else would ever set it back — path-recovery checks and updates keep failing until the routes are reinstalled",
		repairFailed: "routes_ready: did not reinstall the routes: %v",
		repaired:     "routes_ready: reinstalled the capture routes",
	}, func(context.Context) (bool, bool, error) {
		return ready(), true, nil
	}, repair, tick)
}
