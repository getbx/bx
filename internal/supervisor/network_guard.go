package supervisor

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/getbx/bx/internal/stats"
)

type networkGuard struct {
	value atomic.Value // []stats.Warning
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
	g.value.Store(collectNetworkWarnings(ctx))
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
