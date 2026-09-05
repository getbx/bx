package supervisor

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/getbx/bx/internal/confirm"
)

// 服务器旁路路由自愈。
//
// 真机 2026-09-04:电量耗尽休眠 4.5 小时,唤醒后 en0 重新关联,macOS 把挂在它
// 网关上的路由全冲掉 —— 包括 bx 装的服务器 /32 旁路 —— 而 utun 上的两条 /1 劫持
// 路由照旧活着。于是隧道子进程到 VPS 的连接进了 TUN,被 bx 当成普通公网连接:
// 隧道不健康 → kill-switch 拦下 → sing-box 2 毫秒内收到 EOF,隧道自己的重连永远
// 走这条坏路。同一个 Wi-Fi,generation 没变,路径恢复的边沿触发不响;direct_egress
// 那条循环 17:51:22 修好了 scoped 默认路由,对 /32 一无所知。13 分钟后用户 down/up
// 才救回来。**bx 的服务器防环靠的是内核路由,不是 socket mark**(brook/sing-box
// 是子进程),所以那条路由丢了就是成环,而成环是静默的。
//
// 修法照抄 direct_egress:去问内核「发往服务器的包此刻走哪个接口」。

// routeLookup 是一次 `route -n get <服务器 IP>` 的结果。
type routeLookup struct {
	Addr      string
	Interface string
	Err       error
}

// decideServerBypassIntact 是这项观测的**全部判定**。三态:完好 / 成环 / 问不出来。
//
// 「问不出来」绝不压成「坏了」:基于不知道去改路由,是拿一个诊断功能去动真实
// 网络。没有服务器可查同样是不知道 —— 那不是「完好」。
func decideServerBypassIntact(tunName string, lookups []routeLookup) (intact bool, known bool, err error) {
	if len(lookups) == 0 {
		return false, false, nil
	}
	for _, l := range lookups {
		if l.Err != nil {
			return false, false, fmt.Errorf("route get %s: %w", l.Addr, l.Err)
		}
		if strings.EqualFold(strings.TrimSpace(l.Interface), tunName) {
			return false, true, nil
		}
	}
	return true, true, nil
}

// watchServerBypassRoutes 是那条循环;probe/repair 注入,与 watchDirectEgress 同体。
func watchServerBypassRoutes(ctx context.Context, probe egressProbe, repair egressRepair, tick <-chan time.Time) {
	watchKernelRoute(ctx, routeWatchMessages{
		recovered:    "server_bypass 恢复:发往服务器的包又走物理网卡了",
		broken:       "server_bypass 断了:发往服务器的包在走 bx 自己的 TUN(旁路路由不见了,多半是休眠唤醒/网卡重连冲掉的)—— 隧道成环,永远连不上",
		repairFailed: "server_bypass 重新落实路由失败: %v",
		repaired:     "server_bypass 已重新落实路由",
	}, probe, repair, tick)
}

// serverBypassCheckInterval 与 direct_egress 同节拍:这个故障一旦发生就一直在。
const serverBypassCheckInterval = egressCheckInterval

// reassertRoutes 在控制面的锁里重新落实全部路由(Rehijack 的 apply)。
//
// 有待确认的改动时让路:那次改动的 apply 里自带一次 rehijack,交错就是两份
// 路由计划互相拆台。不持锁去 Rehijack 是个看起来很合理的简化,别做。
func (cs *controlServer) reassertRoutes(_ context.Context) error {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if cs.eng.State() == confirm.StateArmed {
		return nil
	}
	apply, _, err := cs.mut.Rehijack()
	if err != nil {
		return fmt.Errorf("准备重新落实路由: %w", err)
	}
	if err := apply(); err != nil {
		return fmt.Errorf("重新落实路由: %w", err)
	}
	return nil
}
