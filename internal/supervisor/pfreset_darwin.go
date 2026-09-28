//go:build darwin

package supervisor

import (
	"context"
	"log"
	"time"

	"github.com/getbx/bx/internal/appattr"
	"github.com/getbx/bx/internal/pfreset"
)

// pfResetDeadline 是重置最多阻塞启动的时间;到点还没清零的交给两段式去点名。
const pfResetDeadline = 10 * time.Second

// runPFReset 在 Hijack 成功之后跑一次。观测用的判据与网络守卫那条告警是同一份:
// appattr.StrayConnections + 同一组 routedAround。
func runPFReset(ctx context.Context, mode string, serverBypass, userBypass []string) {
	decision := decidePFReset(mode)
	if decision == pfResetSkip {
		return
	}
	_, device, err := PhysicalDefaultRoute(ctx)
	if err != nil || device == "" {
		log.Printf("pf reset skipped: no physical default route (%v)", err)
		return
	}
	around := pfResetPrefixes(serverBypass, userBypass)
	observe := func() int {
		pcbs, physical := darwinStraySnapshot(device)
		skip := func(pid int32) bool { return isOwnProcess(pid) || !processAlive(pid) }
		return len(appattr.StrayConnections(pcbs, physical, skip, around))
	}
	if decision == pfResetDryRun {
		log.Printf("pf reset dry-run: %d connection(s) would be reset on %s with these rules:\n%s", observe(), device, pfreset.Rules(device, around))
		return
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	out := pfreset.Run(ctx, pfreset.NewDriver(PFTokenPath()), pfreset.Options{
		Device: device, RoutedAround: around, Observe: observe, Tick: ticker.C, Deadline: pfResetDeadline, Log: log.Printf,
	})
	if out.Err != nil {
		log.Printf("pf reset: %v (initial %d, remaining %d)", out.Err, out.Initial, out.Remaining)
	}
}

// flushStalePFReset 是拆除台账里那一条:Core 正常退出时 Run 的 defer 已经拆过,这里
// 只是兜底(什么都没有时一个破坏性的 pfctl 都不调)。
func flushStalePFReset() {
	if flushed, err := pfreset.FlushStaleDarwin(context.Background(), PFTokenPath()); flushed || err != nil {
		log.Printf("pf reset: stale anchor flushed=%v err=%v", flushed, err)
	}
}
