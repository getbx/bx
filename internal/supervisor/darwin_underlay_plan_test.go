package supervisor

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"strings"
	"testing"
)

func TestDarwinUnderlayPlanChangesOnlyExactBypasses(t *testing.T) {
	old := mustUnderlaySnapshot(t, "en0", "::ffff:192.168.50.2", "192.168.50.27/24")
	next := mustUnderlaySnapshot(t, "en1", "192.168.1.1", "192.168.1.42/24")

	plan, err := darwinUnderlayPlan(old, next,
		[]string{"203.0.113.9/32", "203.0.113.9/32"},
		[]string{"10.44.0.0/16", "10.44.0.0/16", "192.200.0.101/32"},
	)
	if err != nil {
		t.Fatal(err)
	}

	got := darwinUnderlayCommandTexts(plan)
	want := []string{
		"route -n change -net 10.0.0.0/8 192.168.1.1",
		"route -n change -net 10.44.0.0/16 192.168.1.1",
		"route -n change -net 172.16.0.0/12 192.168.1.1",
		"route -n change -net 192.168.0.0/16 192.168.1.1",
		"route -n change -net 192.200.0.101/32 192.168.1.1",
		"route -n change -net 203.0.113.9/32 192.168.1.1",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("underlay change commands = %#v, want %#v", got, want)
	}
	for _, command := range allDarwinUnderlayCommandTexts(plan) {
		if strings.Contains(command, "192.168.50.2") {
			t.Fatalf("command still targets old gateway: %q", command)
		}
	}
}

func TestDarwinUnderlayPlanUnchangedGenerationDoesNothing(t *testing.T) {
	snapshot := mustUnderlaySnapshot(t, "en0", "192.168.50.2", "192.168.50.27/24")
	plan, err := darwinUnderlayPlan(snapshot, snapshot, []string{"203.0.113.9/32"}, []string{"10.44.0.0/16"})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 0 {
		t.Fatalf("unchanged generation planned mutations: %#v", plan)
	}
}

func TestDarwinUnderlayPlanHonorsAnExplicitUnchangedGeneration(t *testing.T) {
	old := mustUnderlaySnapshot(t, "en0", "192.168.50.2", "192.168.50.27/24")
	next := mustUnderlaySnapshot(t, "en1", "192.168.1.1", "192.168.1.42/24")
	next.Generation = old.Generation

	plan, err := darwinUnderlayPlan(old, next, []string{"203.0.113.9/32"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 0 {
		t.Fatalf("unchanged generation planned mutations: %#v", plan)
	}
}

func TestDarwinUnderlayPlanFallbackDeletesOnlyMissingExactBypass(t *testing.T) {
	old := mustUnderlaySnapshot(t, "en0", "192.168.50.2", "192.168.50.27/24")
	next := mustUnderlaySnapshot(t, "en1", "192.168.1.1", "192.168.1.42/24")
	plan, err := darwinUnderlayPlan(old, next, []string{"203.0.113.9/32"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	runner := &recordingCommandRunner{errors: map[string]error{
		"route -n change -net 203.0.113.9/32 192.168.1.1": errors.New("route: not in table"),
		"route -n delete -net 203.0.113.9/32":             errors.New("route: not in table"),
	}}
	if err := executeDarwinUnderlayPlan(context.Background(), runner, plan); err != nil {
		t.Fatalf("fallback failed: %v", err)
	}

	got := runner.commandsFor("203.0.113.9/32")
	want := []string{
		"route -n change -net 203.0.113.9/32 192.168.1.1",
		"route -n delete -net 203.0.113.9/32",
		"route -n add -net 203.0.113.9/32 192.168.1.1",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("missing-route fallback = %#v, want %#v", got, want)
	}
}

func TestDarwinUnderlayPlanNeverTouchesCaptureRoutes(t *testing.T) {
	old := mustUnderlaySnapshot(t, "en0", "192.168.50.2", "192.168.50.27/24")
	next := mustUnderlaySnapshot(t, "en1", "192.168.1.1", "192.168.1.42/24")
	plan, err := darwinUnderlayPlan(old, next,
		[]string{"203.0.113.9/32"},
		[]string{"10.44.0.0/16", "192.200.0.101/32"},
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range allDarwinUnderlayCommandTexts(plan) {
		for _, forbidden := range []string{"0.0.0.0/1", "128.0.0.0/1", "::/1", "8000::/1"} {
			if strings.Contains(command, forbidden) {
				t.Fatalf("capture mutation: %s", command)
			}
		}
	}
}

func TestDarwinUnderlayPlanRejectsUnsafeBypasses(t *testing.T) {
	old := mustUnderlaySnapshot(t, "en0", "192.168.50.2", "192.168.50.27/24")
	next := mustUnderlaySnapshot(t, "en1", "192.168.1.1", "192.168.1.42/24")
	for _, bypass := range []string{
		"203.0.113.9",
		"203.0.113.9/24",
		"0.0.0.0/0",
		"0.0.0.0/1",
		"128.0.0.0/1",
		"::/1",
		"8000::/1",
	} {
		t.Run(bypass, func(t *testing.T) {
			if _, err := darwinUnderlayPlan(old, next, []string{bypass}, nil); err == nil {
				t.Fatalf("accepted unsafe server bypass %q", bypass)
			}
		})
	}
	if _, err := darwinUnderlayPlan(old, next, nil, []string{"203.0.113.9/24"}); err == nil {
		t.Fatal("accepted malformed user bypass")
	}
}

func TestUnderlaySnapshotCanonicalizationAndGeneration(t *testing.T) {
	first := mustUnderlaySnapshot(t, " en0 ", "::ffff:192.168.50.2", "192.168.50.27/24", "10.0.0.9/8", "192.168.50.0/24")
	second := mustUnderlaySnapshot(t, "en0", "192.168.50.2", "192.168.50.0/24", "10.0.0.0/8")
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("canonical snapshots differ: %#v != %#v", first, second)
	}
	if first.Generation == "" || len(first.Generation) != 16 {
		t.Fatalf("generation = %q, want 16 hex characters", first.Generation)
	}
}

func TestUnderlaySnapshotRejectsNonPhysicalUnderlay(t *testing.T) {
	for _, test := range []struct {
		name          string
		interfaceName string
		gateway       string
	}{
		{name: "empty interface", interfaceName: "", gateway: "192.168.1.1"},
		{name: "utun interface", interfaceName: "utun7", gateway: "192.168.1.1"},
		{name: "loopback interface", interfaceName: "lo0", gateway: "192.168.1.1"},
		{name: "ipv6 gateway", interfaceName: "en0", gateway: "2001:db8::1"},
		{name: "loopback gateway", interfaceName: "en0", gateway: "127.0.0.1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			gateway := netip.MustParseAddr(test.gateway)
			if _, err := newUnderlaySnapshot(test.interfaceName, gateway, nil); err == nil {
				t.Fatal("accepted a non-physical underlay")
			}
		})
	}
}

func TestExecuteDarwinUnderlayPlanStopsOnFailure(t *testing.T) {
	old := mustUnderlaySnapshot(t, "en0", "192.168.50.2", "192.168.50.27/24")
	next := mustUnderlaySnapshot(t, "en1", "192.168.1.1", "192.168.1.42/24")
	plan, err := darwinUnderlayPlan(old, next, []string{"203.0.113.9/32"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	runner := &recordingCommandRunner{errors: map[string]error{
		"route -n change -net 10.0.0.0/8 192.168.1.1": errors.New("permission denied"),
	}}
	err = executeDarwinUnderlayPlan(context.Background(), runner, plan)
	if err == nil || !strings.Contains(err.Error(), "underlay_rebind_failed") {
		t.Fatalf("error = %v, want underlay_rebind_failed", err)
	}
	if got, want := runner.calls, []string{"route -n change -net 10.0.0.0/8 192.168.1.1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("runner calls after failure = %#v, want %#v", got, want)
	}
}

func mustUnderlaySnapshot(t *testing.T, interfaceName, gateway string, cidrs ...string) UnderlaySnapshot {
	t.Helper()
	prefixes := make([]netip.Prefix, 0, len(cidrs))
	for _, cidr := range cidrs {
		prefixes = append(prefixes, netip.MustParsePrefix(cidr))
	}
	snapshot, err := newUnderlaySnapshot(interfaceName, netip.MustParseAddr(gateway), prefixes)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func darwinUnderlayCommandTexts(plan []darwinUnderlayCommand) []string {
	got := make([]string, 0, len(plan))
	for _, command := range plan {
		got = append(got, strings.Join(append([]string{command.Name}, command.Args...), " "))
	}
	return got
}

func allDarwinUnderlayCommandTexts(plan []darwinUnderlayCommand) []string {
	var got []string
	for _, command := range plan {
		got = append(got, strings.Join(append([]string{command.Name}, command.Args...), " "))
		for _, fallback := range command.Fallback {
			got = append(got, strings.Join(append([]string{fallback.Name}, fallback.Args...), " "))
		}
	}
	return got
}

type recordingCommandRunner struct {
	calls  []string
	errors map[string]error
}

func (r *recordingCommandRunner) Run(_ context.Context, name string, args ...string) error {
	command := strings.Join(append([]string{name}, args...), " ")
	r.calls = append(r.calls, command)
	return r.errors[command]
}

func (r *recordingCommandRunner) commandsFor(prefix string) []string {
	var commands []string
	for _, call := range r.calls {
		if strings.Contains(call, prefix) {
			commands = append(commands, call)
		}
	}
	return commands
}
