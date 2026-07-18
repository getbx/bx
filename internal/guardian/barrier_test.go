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
	requireCommands(t, apply,
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
	requireCommands(t, cleanup,
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
	})
	if err != nil {
		t.Fatal(err)
	}
	requireCommands(t, release,
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
