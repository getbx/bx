//go:build linux

package guardian

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/getbx/bx/internal/supervisor"
)

// linux 屏障执行器:形状照抄 darwin(计划 → 逐条跑 → 容错判据),命令是
// barrier_iproute.go 的计划器产出的 `ip …`。`ip` 走 PATH 而不钉绝对路径 ——
// 跟 supervisor 的 linux 劫持同一先例(exec.Command("ip", …)),harness 的
// busybox 里 ip 的落点与发行版不同,钉死路径反而两头不讨好。
//
// **幂等性有一处与 darwin 不同,记档**:busybox 的 `ip rule add` 对完全相同的
// 规则**静默重复添加**(iproute2 新版会报 File exists)。Manager 的所有权记账
// 保证不会双装;真撞上重复,多出来的 rule 指向同一张表、行为等价,Remove 各删
// 一条 —— 不为它加「先删后装」,那会把一次真实的权限失败伪装成幂等成功。
type linuxBarrier struct {
	runner CommandRunner
}

func NewBarrier(runner CommandRunner) Barrier {
	if runner == nil {
		runner = linuxRunner{}
	}
	return linuxBarrier{runner: runner}
}

// DiscoverDefaultGateway 复用 supervisor 那份 **metric 感知**的默认路由解析
// (supervisor.LinuxDefaultRoute)。多 WAN 选错 metric 把隧道走上烂路的教训
// (Mudi,SIM+wifi 双默认)只修在那一份里,这里不许手抄第二份解析。
func DiscoverDefaultGateway(ctx context.Context) (string, error) {
	gateway, _, err := supervisor.LinuxDefaultRoute(ctx)
	if err != nil {
		return "", fmt.Errorf("discover default gateway: %w", err)
	}
	return gateway, nil
}

func (b linuxBarrier) Install(ctx context.Context, barrierCtx BarrierContext) error {
	apply, _, _, err := PlanBarrierLinux(barrierCtx)
	if err != nil {
		return err
	}
	return b.run(ctx, apply, isIPRouteExists)
}

func (b linuxBarrier) ReassertBypass(ctx context.Context, barrierCtx BarrierContext) error {
	_, reassert, _, err := PlanBarrierLinux(barrierCtx)
	if err != nil {
		return err
	}
	return b.run(ctx, reassert, isIPRouteExists)
}

func (b linuxBarrier) Release(ctx context.Context, barrierCtx BarrierContext, transferredBypasses []string) error {
	release, err := PlanBarrierReleaseLinux(barrierCtx, transferredBypasses)
	if err != nil {
		return err
	}
	return b.run(ctx, release, isIPRouteMissing)
}

func (b linuxBarrier) Remove(ctx context.Context, barrierCtx BarrierContext) error {
	_, _, cleanup, err := PlanBarrierLinux(barrierCtx)
	if err != nil {
		return err
	}
	return b.run(ctx, cleanup, isIPRouteMissing)
}

// RemoveBlockingBarrierRoutes 是逃生口的清理半边(与 darwin 同名同责):
// 不查任何所有权记录、可无条件跑;「不存在」被容错,真实失败(非 root)上报。
func RemoveBlockingBarrierRoutes(ctx context.Context, runner CommandRunner) error {
	if runner == nil {
		runner = linuxRunner{}
	}
	return linuxBarrier{runner: runner}.run(ctx, PlanLinuxBarrierCleanup(), isIPRouteMissing)
}

func (b linuxBarrier) run(ctx context.Context, planned []Command, tolerated func(error) bool) error {
	for _, command := range planned {
		err := b.runner.Run(ctx, command)
		if err == nil || tolerated(err) {
			continue
		}
		// v6 整族缺席只豁免 -6 命令(判据与范围的理由见 isIPv6FamilyUnsupported)。
		if commandIsIPv6(command) && isIPv6FamilyUnsupported(err) {
			continue
		}
		return fmt.Errorf("run %s: %w", command.String(), err)
	}
	return nil
}

type linuxRunner struct{}

func (linuxRunner) Run(ctx context.Context, command Command) error {
	output, err := exec.CommandContext(ctx, command.Name, command.Args...).CombinedOutput()
	if err != nil {
		return commandOutputError{err: err, output: strings.TrimSpace(string(output))}
	}
	return nil
}
