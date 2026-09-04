package supervisor

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/getbx/bx/internal/confirm"
)

// 服务器旁路重新跟随 DNS。
//
// 此前「什么必须绕开隧道」只在启动时算一次:staticA 把服务器域名钉在启动那一刻
// 的 IP 上,旁路路由 pin 的也是它。VPS 换 IP 之后,隧道子进程经系统 DNS(=bx)
// 拿到的仍是旧 IP,连不上、退避、再连,**永远**。真机 2026-08-06 VPS 换 IP,
// NAS 上的 bx 重连 65638 次、静默断到 09-03 才被人发现。
//
// 修法不另造一份判定:切换服务器那条路早就有「重读配置 → 防环解析 → 发布两半
// → 变了就 rehijack」(newBypassRefresher + handleSetServer),这里只是在
// **隧道持续不健康**时替用户按一次那个按钮,然后重建传输让子进程拿新答案。
//
// 这条只对**用域名**的链接有意义:IP 字面量链接在 resolveAll 里直接短路,
// 跟不跟随都一样 —— 那种配置换 IP 只能改链接。

const (
	// bypassRefollowAfter 是隧道要连续不健康多久才去重跟随。隧道自己的重连退避
	// 上限在这个量级以内,短于它的抖动都还在「自己能好」的范围里。
	bypassRefollowAfter = 2 * time.Minute
	// bypassRefollowInterval 是两次重跟随之间的最小间隔:每次都要问一轮 DNS
	// (经国内 DNS 直连出去),一条 30 秒的循环不限频就是一个 DNS 探针。
	bypassRefollowInterval = 5 * time.Minute
	// bypassRefollowTick 是循环的节拍;只看时长判据,节拍本身不承重。
	bypassRefollowTick = 30 * time.Second
)

// shouldRefollowServerBypass 是这件事的**全部判定**。sinceLast 为负表示还没试过。
func shouldRefollowServerBypass(unhealthyFor, sinceLast time.Duration) bool {
	if unhealthyFor < bypassRefollowAfter {
		return false
	}
	return sinceLast < 0 || sinceLast >= bypassRefollowInterval
}

// watchServerBypass 是那条循环。healthy 是主传输的健康;refollow 是真正去做的
// 那件事(生产 = controlServer.refollowServerBypass 带上当前链接);时间只从
// tick 的值里取(time.Ticker 送的就是当拍时刻),不另开一个时钟 —— 两个时间源
// 在测试里必然赛跑。
func watchServerBypass(ctx context.Context, healthy func() bool, refollow func(context.Context) error, tick <-chan time.Time) {
	var unhealthySince, lastAttempt time.Time
	for {
		var t time.Time
		select {
		case <-ctx.Done():
			return
		case t = <-tick:
		}
		if healthy() {
			unhealthySince = time.Time{}
			continue
		}
		if unhealthySince.IsZero() {
			unhealthySince = t
		}
		sinceLast := time.Duration(-1)
		if !lastAttempt.IsZero() {
			sinceLast = t.Sub(lastAttempt)
		}
		if !shouldRefollowServerBypass(t.Sub(unhealthySince), sinceLast) {
			continue
		}
		lastAttempt = t
		if err := refollow(ctx); err != nil {
			log.Printf("server_bypass_refollow 失败(旁路保持原样): %v", err)
		}
	}
}

// refollowOutcome 是一次重跟随的结局,三态分开报:让路 / 没变 / 变了并已落实。
type refollowOutcome int

const (
	refollowYielded refollowOutcome = iota
	refollowUnchanged
	refollowChanged
)

func (o refollowOutcome) String() string {
	switch o {
	case refollowYielded:
		return "yielded"
	case refollowUnchanged:
		return "unchanged"
	case refollowChanged:
		return "changed"
	}
	return fmt.Sprintf("refollowOutcome(%d)", int(o))
}

// refollowServerBypass 在控制面的锁里重新解析并落实服务器旁路。
//
// 必须持 cs.mu:刷新是**替换**语义,与 handleSetServer 交错会抹掉刚算进去的那台
// (见 TestSetServerSerializesConcurrentBypassRefresh)。有待确认的改动时让路 ——
// 那次改动点名的新服务器可能还没落盘,这里按盘上配置刷新就会把它从旁路里剔掉,
// 而它的路由已经装上了。
//
// 变了才动:先 rehijack(把新 IP 的旁路装上、旧的拆掉),再重建传输(子进程重新
// 解析,拿到 staticA 里的新答案)。反过来是成环窗口:新子进程先连新 IP,而新 IP
// 还没在旁路里,那条连接被劫进 TUN。重建传输不持 cs.mu:它要等新隧道健康,
// 最长一个 healthTimeout,不该把控制面锁那么久。
func (cs *controlServer) refollowServerBypass(ctx context.Context, links []string) (refollowOutcome, error) {
	if cs.refreshBypass == nil {
		return refollowYielded, nil
	}
	cs.mu.Lock()
	if cs.eng.State() == confirm.StateArmed {
		cs.mu.Unlock()
		return refollowYielded, nil
	}
	changed, err := cs.refreshBypass(links)
	if err != nil {
		cs.mu.Unlock()
		return refollowUnchanged, fmt.Errorf("重新解析服务器旁路: %w", err)
	}
	if !changed {
		cs.mu.Unlock()
		return refollowUnchanged, nil
	}
	apply, _, err := cs.mut.Rehijack()
	if err != nil {
		cs.mu.Unlock()
		return refollowUnchanged, fmt.Errorf("准备重装旁路路由: %w", err)
	}
	if err := apply(); err != nil {
		cs.mu.Unlock()
		return refollowUnchanged, fmt.Errorf("重装旁路路由: %w", err)
	}
	cs.mu.Unlock()
	log.Printf("server_bypass_refollow 服务器地址变了,旁路已重装;正在重建传输")
	if err := cs.mut.Reconnect(); err != nil {
		return refollowChanged, fmt.Errorf("旁路已重装,但重建传输失败(隧道自己的重连会继续): %w", err)
	}
	return refollowChanged, nil
}
