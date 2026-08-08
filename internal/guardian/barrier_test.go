package guardian

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
)

func TestPlanBarrierBlocksPublicIPv4MoreSpecificallyThanSplitDefault(t *testing.T) {
	apply, reassert, cleanup, err := PlanBarrier(BarrierContext{
		Gateway:      "192.168.50.2",
		ServerBypass: []string{"23.27.134.77/32"},
		BlockIPv6:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	requireCommands(
		t, apply,
		"route -n add -net 23.27.134.77/32 192.168.50.2",
		"route -n add -net 0.0.0.0/2 127.0.0.1 -reject",
		"route -n add -net 64.0.0.0/2 127.0.0.1 -reject",
		"route -n add -net 128.0.0.0/2 127.0.0.1 -reject",
		"route -n add -net 192.0.0.0/2 127.0.0.1 -reject",
		"route -n add -inet6 -net ::/2 ::1 -reject",
		"route -n add -inet6 -net 4000::/2 ::1 -reject",
		"route -n add -inet6 -net 8000::/2 ::1 -reject",
		"route -n add -inet6 -net c000::/2 ::1 -reject",
	)
	requireCommands(t, reassert, "route -n add -net 23.27.134.77/32 192.168.50.2")
	requireCommands(
		t, cleanup,
		"route -n delete -inet6 -net c000::/2",
		"route -n delete -inet6 -net 8000::/2",
		"route -n delete -inet6 -net 4000::/2",
		"route -n delete -inet6 -net ::/2",
		"route -n delete -net 192.0.0.0/2",
		"route -n delete -net 128.0.0.0/2",
		"route -n delete -net 64.0.0.0/2",
		"route -n delete -net 0.0.0.0/2",
		"route -n delete -net 23.27.134.77/32",
	)
}

func TestPlanBarrierReleaseToCorePreservesTransferredBypass(t *testing.T) {
	release, err := PlanBarrierRelease(BarrierContext{
		Gateway:      "192.168.50.2",
		ServerBypass: []string{"23.27.134.77/32"},
		BlockIPv6:    true,
	}, []string{"23.27.134.77/32"})
	if err != nil {
		t.Fatal(err)
	}
	requireCommands(
		t, release,
		"route -n delete -inet6 -net c000::/2",
		"route -n delete -inet6 -net 8000::/2",
		"route -n delete -inet6 -net 4000::/2",
		"route -n delete -inet6 -net ::/2",
		"route -n delete -net 192.0.0.0/2",
		"route -n delete -net 128.0.0.0/2",
		"route -n delete -net 64.0.0.0/2",
		"route -n delete -net 0.0.0.0/2",
	)
	for _, command := range release {
		if strings.Contains(command.String(), "23.27.134.77/32") {
			t.Fatalf("release deleted transferred bypass: %s", command.String())
		}
	}
}

func TestPlanBarrierReleaseDeletesOldBypassNotTransferredToTarget(t *testing.T) {
	release, err := PlanBarrierRelease(BarrierContext{
		Gateway:      "192.168.50.2",
		ServerBypass: []string{"23.27.134.77/32"},
		BlockIPv6:    true,
	}, []string{"198.51.100.20/32"})
	if err != nil {
		t.Fatal(err)
	}
	if got := release[len(release)-1].String(); got != "route -n delete -net 23.27.134.77/32" {
		t.Fatalf("last release command = %q, want stale Guardian bypass deletion", got)
	}
	for _, command := range release {
		if strings.Contains(command.String(), "198.51.100.20/32") {
			t.Fatalf("release deleted target-owned bypass: %s", command.String())
		}
	}
}

func TestBarrierOwnershipOldAToTargetBThenDownLeavesNoBypass(t *testing.T) {
	oldContext := BarrierContext{Gateway: "192.168.50.2", ServerBypass: []string{"23.27.134.77/32"}, BlockIPv6: true}
	targetContext := BarrierContext{Gateway: "192.168.50.2", ServerBypass: []string{"198.51.100.20/32"}, BlockIPv6: true}
	routes := make(map[string]struct{})
	apply := func(commands []Command) {
		for _, command := range commands {
			cidr := ""
			for index, arg := range command.Args {
				if arg == "-net" && index+1 < len(command.Args) {
					cidr = command.Args[index+1]
					break
				}
			}
			if cidr == "" {
				t.Fatalf("route command has no CIDR: %s", command.String())
			}
			switch command.Args[1] {
			case "add":
				routes[cidr] = struct{}{}
			case "delete":
				delete(routes, cidr)
			default:
				t.Fatalf("unsupported route command: %s", command.String())
			}
		}
	}

	oldApply, _, _, err := PlanBarrier(oldContext)
	if err != nil {
		t.Fatal(err)
	}
	apply(oldApply)
	routes[targetContext.ServerBypass[0]] = struct{}{} // target Core owns B
	release, err := PlanBarrierRelease(oldContext, targetContext.ServerBypass)
	if err != nil {
		t.Fatal(err)
	}
	apply(release)
	if _, exists := routes[oldContext.ServerBypass[0]]; exists {
		t.Fatalf("old Guardian bypass survived target handoff: %#v", routes)
	}
	if _, exists := routes[targetContext.ServerBypass[0]]; !exists {
		t.Fatalf("target bypass was not preserved: %#v", routes)
	}

	downApply, _, downCleanup, err := PlanBarrier(targetContext)
	if err != nil {
		t.Fatal(err)
	}
	apply(downApply)
	delete(routes, targetContext.ServerBypass[0]) // Core teardown releases B
	apply(downCleanup)
	if len(routes) != 0 {
		t.Fatalf("old A -> target B -> Down leaked routes: %#v", routes)
	}
}

func TestPlanBarrierRejectsUnsafeHandoffs(t *testing.T) {
	for _, context := range []BarrierContext{
		{Gateway: "not-an-ip", ServerBypass: []string{"23.27.134.77/32"}},
		{Gateway: "2001:db8::1", ServerBypass: []string{"23.27.134.77/32"}},
		{Gateway: "192.168.1.1"},
		{Gateway: "192.168.1.1", ServerBypass: []string{"0.0.0.0/0"}},
		{Gateway: "192.168.1.1", ServerBypass: []string{"23.27.134.0/24"}},
		{Gateway: "192.168.1.1", ServerBypass: []string{"example.com"}},
		{Gateway: "192.168.1.1", ServerBypass: []string{"2001:db8::7/128"}},
	} {
		if _, _, _, err := PlanBarrier(context); err == nil {
			t.Fatalf("unsafe handoff accepted: %+v", context)
		}
	}
}

func TestParseDefaultGatewayRejectsMissingOrNonIPv4Gateway(t *testing.T) {
	for _, output := range []string{
		"   gateway: 192.168.50.1\n",
		"gateway: not-an-ip\n",
		"gateway: 2001:db8::1\n",
		"interface: en0\n",
	} {
		gateway, err := parseDefaultGateway([]byte(output))
		if strings.Contains(output, "192.168.50.1") {
			if err != nil || gateway != "192.168.50.1" {
				t.Fatalf("gateway = %q, %v", gateway, err)
			}
			continue
		}
		if err == nil {
			t.Fatalf("unsafe gateway accepted from %q", output)
		}
	}
}

func TestParseDefaultGatewayNamesPointToPointInterface(t *testing.T) {
	_, err := parseDefaultGateway([]byte("   route to: default\ndestination: default\n  interface: utun16\n"))
	if err == nil {
		t.Fatal("want error")
	}
	for _, want := range []string{"utun16", "point-to-point"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q missing %q", err, want)
		}
	}
}

func TestDarwinBarrierUsesValidatedRouteArgvAndIdempotentErrors(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Darwin executor is unavailable on this platform")
	}

	runner := &recordingRunner{errors: []error{
		errors.New("route: writing to routing socket: File exists"),
		errors.New("route: writing to routing socket: File exists"),
		nil,
		nil,
		nil,
		errors.New("route: writing to routing socket: File exists"),
		errors.New("route: writing to routing socket: not in table"),
	}}
	barrier := NewBarrier(runner)
	ctx := BarrierContext{Gateway: "192.168.50.2", ServerBypass: []string{"23.27.134.77/32"}}

	if err := barrier.Install(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	if err := barrier.ReassertBypass(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	if err := barrier.Remove(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	if len(runner.commands) != 11 {
		t.Fatalf("commands = %d, want 11", len(runner.commands))
	}
	for _, command := range runner.commands {
		if command.Name != "/sbin/route" {
			t.Fatalf("command name = %q, want /sbin/route", command.Name)
		}
	}
	if got := runner.commands[0].String(); got != "/sbin/route -n add -net 23.27.134.77/32 192.168.50.2" {
		t.Fatalf("first command = %q", got)
	}
	if got := runner.commands[len(runner.commands)-1].String(); got != "/sbin/route -n delete -net 23.27.134.77/32" {
		t.Fatalf("last command = %q", got)
	}
}

type recordingRunner struct {
	commands []Command
	errors   []error
}

func (r *recordingRunner) Run(_ context.Context, command Command) error {
	r.commands = append(r.commands, command)
	if len(r.errors) == 0 {
		return nil
	}
	err := r.errors[0]
	r.errors = r.errors[1:]
	return err
}

func requireCommands(t *testing.T, commands []Command, want ...string) {
	t.Helper()
	if len(commands) != len(want) {
		t.Fatalf("commands = %d, want %d: %v", len(commands), len(want), commands)
	}
	for i, command := range commands {
		if got := command.String(); got != want[i] {
			t.Errorf("command %d = %q, want %q", i, got, want[i])
		}
	}
}

// 强制拆除路径(`bx down` 在 Guardian 不可达时 launchctl bootout)会连同 Guardian
// 进程一起抹掉内存里的 barrierOwnership,但内核里的 reject 路由还在——它们的前缀
// (/2)比 Core 的 split-default(/1)更长,压过一切,整机零连通;此后 up 的新
// Guardian 因为 barrierOwnership 为空而认为"无屏障",down 的 removeBarrier 直接
// no-op,孤儿屏障在 up/down/uninstall 全周期存活。所以清理必须**不依赖任何
// BarrierContext / 所有权记录**,并覆盖全部 v4+v6 阻断段。
func TestPlanBlockingBarrierCleanupDeletesEveryBlockingRouteWithoutContext(t *testing.T) {
	requireCommands(
		t, PlanBlockingBarrierCleanup(),
		"route -n delete -inet6 -net c000::/2",
		"route -n delete -inet6 -net 8000::/2",
		"route -n delete -inet6 -net 4000::/2",
		"route -n delete -inet6 -net ::/2",
		"route -n delete -net 192.0.0.0/2",
		"route -n delete -net 128.0.0.0/2",
		"route -n delete -net 64.0.0.0/2",
		"route -n delete -net 0.0.0.0/2",
	)
}

// 守卫:以后谁往 publicIPv4Blocks/publicIPv6Blocks 里加阻断段,清理必须自动跟上,
// 否则又会留下删不掉的孤儿路由。
func TestPlanBlockingBarrierCleanupCoversEveryDeclaredBlock(t *testing.T) {
	commands := PlanBlockingBarrierCleanup()
	for _, block := range append(append([]string(nil), publicIPv4Blocks...), publicIPv6Blocks...) {
		found := false
		for _, command := range commands {
			if strings.HasSuffix(command.String(), " "+block) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("阻断段 %s 没有对应的清理命令: %v", block, commands)
		}
	}
}

func TestRemoveBlockingBarrierRoutesDeletesAllBlocksAndToleratesMissing(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Darwin executor is unavailable on this platform")
	}
	// 孤儿清理是无条件运行的:大多数情况下路由并不存在,"not in table" 必须被容忍,
	// 否则第一条不存在的路由就会中断后面 7 条真正需要删除的命令。
	runner := &recordingRunner{errors: []error{
		errors.New("route: writing to routing socket: not in table"),
		nil,
		errors.New("route: writing to routing socket: not in table"),
	}}
	if err := RemoveBlockingBarrierRoutes(context.Background(), runner); err != nil {
		t.Fatalf("清理孤儿屏障路由不应因缺失路由而失败: %v", err)
	}
	requireCommands(
		t, runner.commands,
		"/sbin/route -n delete -inet6 -net c000::/2",
		"/sbin/route -n delete -inet6 -net 8000::/2",
		"/sbin/route -n delete -inet6 -net 4000::/2",
		"/sbin/route -n delete -inet6 -net ::/2",
		"/sbin/route -n delete -net 192.0.0.0/2",
		"/sbin/route -n delete -net 128.0.0.0/2",
		"/sbin/route -n delete -net 64.0.0.0/2",
		"/sbin/route -n delete -net 0.0.0.0/2",
	)
}

func TestRemoveBlockingBarrierRoutesReportsGenuineFailure(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Darwin executor is unavailable on this platform")
	}
	runner := &recordingRunner{errors: []error{errors.New("route: writing to routing socket: Operation not permitted")}}
	if err := RemoveBlockingBarrierRoutes(context.Background(), runner); err == nil {
		t.Fatal("真实失败(如非 root)必须报出来,否则用户以为路由已清理")
	}
}
