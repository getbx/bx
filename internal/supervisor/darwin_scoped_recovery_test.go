//go:build darwin

package supervisor

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"
)

// **换网之后 scoped 默认路由必须被重新装上,否则 bx 自己的直连整个死掉。**
//
// 真机(2026-08-13):机器从 192.168.50.x 换到 10.84.6.x,旧网关消失、内核清掉了
// 那条 scoped 默认路由,而路径恢复**没有把它装回来** —— `bx status` 随即报
// `*.qq.com 强制直连 1443 条,失败 1236(86%)`,而保护状态一直显示 Protected。
//
// 根因是这个仓库最典型的那个形状:**路由计划有两份。** `darwinRouteSpecs`
// (Hijack / RehijackRoutes 用)与 `darwinUnderlayPlan`(路径恢复用)各自独立,
// 而修复只加进了第一份。第二份**刻意**不调第一份(它不该碰捕获路由),
// 于是没有任何东西保证两者对「scoped 默认路由」这件事的看法一致。
func TestRecoveryReinstallsTheScopedDefaultOnTheNewGateway(t *testing.T) {
	plan, err := darwinUnderlayPlan(
		snap("en0", "192.168.50.2", "192.168.50.0/24"),
		snap("en0", "10.84.6.1", "10.84.6.0/23"),
		[]string{"166.1.190.123/32"}, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	var scoped *darwinUnderlayCommand
	for i := range plan {
		if strings.Contains(strings.Join(plan[i].Args, " "), "-ifscope") {
			scoped = &plan[i]
		}
	}
	if scoped == nil {
		t.Fatalf("换网计划里没有 scoped 默认路由 —— 换网后 bx 的直连会整个失效:\n%v", planText(plan))
	}
	joined := strings.Join(scoped.Args, " ") + " " + fallbackText(*scoped)
	for _, want := range []string{"-ifscope", "en0", "default", "10.84.6.1"} {
		if !strings.Contains(joined, want) {
			t.Errorf("scoped 命令缺 %q:%s", want, joined)
		}
	}
	// **必须指向新网关,不能是旧的** —— 装一条指向已消失网关的路由比不装更糟。
	if strings.Contains(joined, "192.168.50.2") {
		t.Errorf("scoped 路由还指着旧网关:%s", joined)
	}
}

// **两份路由计划对「有没有 scoped 默认路由」必须一致。**
//
// 这条守卫钉的正是本次缺陷的形状:一份加了、另一份没加,而两边的测试都绿。
func TestBothDarwinRoutePlansAgreeOnTheScopedDefault(t *testing.T) {
	hijack := darwinRouteSpecs("utun0", "10.84.6.1", "en0", nil, []string{"1.2.3.4/32"}, nil, false)
	hijackHas := false
	for _, s := range hijack {
		if strings.Contains(strings.Join(s.add, " "), "-ifscope") {
			hijackHas = true
		}
	}
	recovery, err := darwinUnderlayPlan(
		snap("en0", "192.168.50.2", "192.168.50.0/24"),
		snap("en0", "10.84.6.1", "10.84.6.0/23"),
		[]string{"1.2.3.4/32"}, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	recoveryHas := false
	for _, c := range recovery {
		if strings.Contains(strings.Join(c.Args, " "), "-ifscope") {
			recoveryHas = true
		}
	}
	if hijackHas != recoveryHas {
		t.Fatalf("两份路由计划分叉了:Hijack 装 scoped=%v,路径恢复装 scoped=%v —— "+
			"这正是本次真机缺陷的形状(一份加了、另一份没加,而两边测试都绿)",
			hijackHas, recoveryHas)
	}
}

// **scoped 路由装不上不许拖垮整次恢复。**
//
// 恢复的首要职责是把流量重新引对;那条 scoped 路由只影响 bx 自己的直连。
// 让它把恢复整个失败掉,是拿一个次要目标去赌主要目标 —— 与「停止路径不许依赖
// 别的先成功」同一条纪律。
func TestScopedDefaultFailureDoesNotFailTheWholeRecovery(t *testing.T) {
	plan, err := darwinUnderlayPlan(
		snap("en0", "192.168.50.2", "192.168.50.0/24"),
		snap("en0", "10.84.6.1", "10.84.6.0/23"),
		[]string{"1.2.3.4/32"}, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	runner := &scriptedRunner{fail: func(args []string) error {
		if strings.Contains(strings.Join(args, " "), "-ifscope") {
			return errors.New("route: writing to routing socket: File exists")
		}
		return nil
	}}
	if err := executeDarwinUnderlayPlan(context.Background(), runner, plan); err != nil {
		t.Fatalf("scoped 路由失败拖垮了整次恢复:%v", err)
	}
	// 反面:旁路那些**不是**可选的,它们失败仍然必须让恢复失败。
	hard := &scriptedRunner{fail: func(args []string) error { return errors.New("boom") }}
	if err := executeDarwinUnderlayPlan(context.Background(), hard, plan); err == nil {
		t.Fatal("旁路路由全失败却报告恢复成功")
	}
}

type scriptedRunner struct{ fail func([]string) error }

func (s *scriptedRunner) Run(_ context.Context, name string, args ...string) error {
	return s.fail(append([]string{name}, args...))
}

func planText(plan []darwinUnderlayCommand) []string {
	out := make([]string, 0, len(plan))
	for _, c := range plan {
		out = append(out, c.Name+" "+strings.Join(c.Args, " "))
	}
	return out
}

func fallbackText(c darwinUnderlayCommand) string {
	var parts []string
	for _, f := range c.Fallback {
		parts = append(parts, strings.Join(f.Args, " "))
	}
	return strings.Join(parts, " | ")
}

func snap(iface, gw, cidr string) UnderlaySnapshot {
	return UnderlaySnapshot{
		Interface:  iface,
		Gateway:    netip.MustParseAddr(gw),
		LocalCIDRs: []netip.Prefix{netip.MustParsePrefix(cidr)},
	}
}
