package supervisor

import (
	"context"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/getbx/bx/internal/overlay"
	"github.com/getbx/bx/internal/policy"
	"github.com/getbx/bx/internal/stats"
)

type networkGuard struct {
	value atomic.Value // []stats.Warning
	// routedAround 回答「bx 自己在路由上绕开了哪些网段」(服务器旁路 + 用户 bypass),
	// 每次刷新现问 —— 切过服务器之后旧那台不再免检、新那台不能被点名。
	routedAround func() []netip.Prefix
	// collect 是平台的共存检测(collectNetworkWarnings);做成字段只为让「刷新时递给
	// 它的是 routedAround 本尊」这一跳可测 —— 递 nil 全仓照样编译。
	collect func(context.Context, func() []netip.Prefix) []stats.Warning
	// baseline 是**第一次刷新时**在跑的 overlay 集合。每轮与现状比对,晚到的租户
	// 要报出来 —— 它的 DNS split 拿不到(见 lateTenantWarning)。
	//
	// **刻意在这里自己采,而不是由 Run 传进来**:serveControl 已经有 14 个位置参数,
	// 其中 4 个相邻 string —— 再加一个,传错位置照样编译。而这个守卫本来就在启动
	// 阶段起来,第一次刷新的时机与 Run 里那次检测等价。
	baselineOnce sync.Once
	baseline     []overlay.Tenant
}

// networkGuardForServe 是控制面那一侧的组装:守卫要知道 bx 自己绕开了哪些网段,
// 而那两份清单都在 controlServeOptions 里。
func networkGuardForServe(ctx context.Context, opts controlServeOptions) *networkGuard {
	return startNetworkGuard(ctx, routedAroundForGuard(opts))
}

// routedAroundForGuard 把「bx 自己绕开隧道的网段」组合成一份:Runtime().ServerBypass
// (现算的服务器旁路,切服务器之后跟着变)加上配置的 bypass:(裸 IP 与 Hijack 一样
// 补成 /32,认不出的条目丢掉、不编)。真机 2026-09-28:ssh 跳板连 bx 自己的 VPS 被
// 报成「绕过 bx」,就是这份清单没递到判据手上。
func routedAroundForGuard(opts controlServeOptions) func() []netip.Prefix {
	return func() []netip.Prefix {
		var entries []string
		if opts.Runtime != nil {
			entries = append(entries, opts.Runtime().ServerBypass...)
		}
		entries = append(entries, opts.UserBypass...)
		out := make([]netip.Prefix, 0, len(entries))
		for _, e := range entries {
			if p, ok := policy.RuleCIDR(e); ok {
				out = append(out, p)
			}
		}
		return out
	}
}

func startNetworkGuard(ctx context.Context, routedAround func() []netip.Prefix) *networkGuard {
	if routedAround == nil {
		routedAround = func() []netip.Prefix { return nil }
	}
	g := &networkGuard{routedAround: routedAround, collect: collectNetworkWarnings}
	g.value.Store([]stats.Warning(nil))
	g.refresh(ctx)
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				g.refresh(ctx)
			}
		}
	}()
	return g
}

func (g *networkGuard) refresh(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, 4*time.Second)
	defer cancel()
	warnings := g.collect(ctx, g.routedAround)
	// 平台无关的那一条:有没有 overlay 是 bx 起来之后才跑的。
	now := detectOverlayTenants()
	g.baselineOnce.Do(func() { g.baseline = now })
	if late := lateTenantWarning(tenantsAppearedSince(g.baseline, now)); late.Name != "" {
		warnings = append(warnings, late)
	}
	g.value.Store(warnings)
}

func (g *networkGuard) warnings() []stats.Warning {
	if g == nil {
		return nil
	}
	warnings, _ := g.value.Load().([]stats.Warning)
	if len(warnings) == 0 {
		return nil
	}
	out := make([]stats.Warning, len(warnings))
	copy(out, warnings)
	return out
}
