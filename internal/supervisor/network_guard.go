package supervisor

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/getbx/bx/internal/overlay"
	"github.com/getbx/bx/internal/stats"
)

type networkGuard struct {
	value atomic.Value // []stats.Warning
	// baseline 是**第一次刷新时**在跑的 overlay 集合。每轮与现状比对,晚到的租户
	// 要报出来 —— 它的 DNS split 拿不到(见 lateTenantWarning)。
	//
	// **刻意在这里自己采,而不是由 Run 传进来**:serveControl 已经有 14 个位置参数,
	// 其中 4 个相邻 string —— 再加一个,传错位置照样编译。而这个守卫本来就在启动
	// 阶段起来,第一次刷新的时机与 Run 里那次检测等价。
	baselineOnce sync.Once
	baseline     []overlay.Tenant
}

func startNetworkGuard(ctx context.Context) *networkGuard {
	g := &networkGuard{}
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
	warnings := collectNetworkWarnings(ctx)
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
