//go:build darwin

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/guardian"
	"github.com/getbx/bx/internal/stats"
	"github.com/getbx/bx/internal/supervisor"
)

func TestLegacyMigrationRequestPrefersRuntimeHandoffMetadata(t *testing.T) {
	loadCalls := 0
	request, err := legacyMigrationRequest(context.Background(), "/etc/bx/config.yaml", migrationMetadataDeps{
		discoverGateway: func(context.Context) (string, error) { return "192.0.2.1", nil },
		fetchRuntime: func(string) (supervisor.RuntimeState, error) {
			return supervisor.RuntimeState{
				PID: 42, TunName: "utun7", SocksAddr: "127.0.0.1:1080",
				ServerBypass: []string{"198.51.100.10/32"}, TunnelHealthy: true,
				DNSListening: true, RoutesInstalled: true,
			}, nil
		},
		loadConfig: func(string) (*config.Config, error) {
			loadCalls++
			return nil, errors.New("fallback must not run")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if loadCalls != 0 {
		t.Fatalf("runtime handoff unexpectedly read config %d times", loadCalls)
	}
	if request.Gateway != "192.0.2.1" || strings.Join(request.ServerBypass, ",") != "198.51.100.10/32" {
		t.Fatalf("migration request = %+v", request)
	}
}

func TestLegacyMigrationRequestFallbackResolvesOnlyTransportHostIPs(t *testing.T) {
	secret := "vless://super-secret-uuid@proxy.example:443?security=reality&pbk=secret-key"
	cfg := &config.Config{
		Server:     secret,
		Transports: []string{secret, "hysteria2://secret-password@udp.example:8443"},
		UDP:        config.UDP{Mode: "proxy", Transport: "trojan://another-secret@tcp.example:443"},
	}
	lookups := map[string][]netip.Addr{
		"proxy.example": {netip.MustParseAddr("198.51.100.10"), netip.MustParseAddr("2001:db8::10")},
		"udp.example":   {netip.MustParseAddr("198.51.100.11")},
		"tcp.example":   {netip.MustParseAddr("198.51.100.12")},
	}
	request, err := legacyMigrationRequest(context.Background(), "/etc/bx/config.yaml", migrationMetadataDeps{
		discoverGateway: func(context.Context) (string, error) { return "192.0.2.1", nil },
		fetchRuntime:    func(string) (supervisor.RuntimeState, error) { return supervisor.RuntimeState{}, errors.New("404") },
		loadConfig:      func(string) (*config.Config, error) { return cfg, nil },
		lookupIP: func(_ context.Context, host string) ([]netip.Addr, error) {
			return lookups[host], nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"super-secret", "secret-password", "another-secret", "vless://", "hysteria2://", "trojan://"} {
		if strings.Contains(string(b), forbidden) {
			t.Fatalf("migration request leaked %q: %s", forbidden, b)
		}
	}
	want := "198.51.100.10/32,2001:db8::10/128,198.51.100.11/32,198.51.100.12/32"
	if got := strings.Join(request.ServerBypass, ","); got != want {
		t.Fatalf("migration bypasses = %q, want %q", got, want)
	}
}

func TestLegacyMigrationRequestRejectsInvalidRuntimeWithoutSecretFallback(t *testing.T) {
	loadCalls := 0
	_, err := legacyMigrationRequest(context.Background(), "/etc/bx/config.yaml", migrationMetadataDeps{
		discoverGateway: func(context.Context) (string, error) { return "192.0.2.1", nil },
		fetchRuntime: func(string) (supervisor.RuntimeState, error) {
			return supervisor.RuntimeState{PID: 42, ServerBypass: []string{"198.51.100.0/24"}}, nil
		},
		loadConfig: func(string) (*config.Config, error) {
			loadCalls++
			return nil, nil
		},
	})
	if err == nil {
		t.Fatal("invalid runtime handoff accepted")
	}
	if loadCalls != 0 {
		t.Fatal("invalid runtime metadata triggered raw-config fallback")
	}
}

func TestMacOSUpLifecycleMigratesBeforeMenuAndWaitsForProtected(t *testing.T) {
	var events []string
	client := &recordingGuardianClient{
		events:        &events,
		migrateStatus: guardian.Status{Protection: guardian.ProtectionStarting},
		statuses:      []guardian.Status{{Desired: guardian.DesiredOn, Phase: guardian.PhaseCommitted, Protection: guardian.ProtectionProtected}},
	}
	deps := testMacOSLifecycleDeps(&events, client)
	deps.legacyInstalled = func() bool { return true }
	deps.legacyLoaded = func() (bool, error) {
		events = append(events, "legacy.loaded")
		return true, nil
	}
	deps.migrationRequest = func(context.Context, string) (guardian.MigrationRequest, error) {
		events = append(events, "metadata")
		return guardian.MigrationRequest{Gateway: "192.0.2.1", ServerBypass: []string{"198.51.100.10/32"}}, nil
	}
	result, err := macOSUpLifecycle(context.Background(), "/etc/bx/config.yaml", deps)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"legacy.loaded", "metadata", "guardian.enable", "guardian.ready", "guardian.migrate", "guardian.status", "console.uid", "menu.ensure"}
	if strings.Join(events, "|") != strings.Join(want, "|") {
		t.Fatalf("macOS up events = %#v, want %#v", events, want)
	}
	if result.Status.Protection != guardian.ProtectionProtected || result.MenuWarning != nil {
		t.Fatalf("macOS up result = %+v", result)
	}
	if client.upCalls != 0 || client.migrateCalls != 1 {
		t.Fatalf("Guardian calls = up:%d migrate:%d", client.upCalls, client.migrateCalls)
	}
}

func TestMacOSUpSummaryIncludesGuardianDNSState(t *testing.T) {
	for _, status := range []guardian.Status{
		{Protection: guardian.ProtectionProtected, DNSState: guardian.DNSUnmanaged, DNSService: "Wi-Fi"},
		{Protection: guardian.ProtectionProtected, DNSState: guardian.DNSUnknown, DNSService: "Wi-Fi"},
		{Protection: guardian.ProtectionProtected},
	} {
		got := renderUpSummary(stats.Report{TunnelHealthy: true, LatencyMS: 18, UDPMode: "proxy"}, status)
		if !strings.Contains(got, "Status     Needs Attention") {
			t.Fatalf("macOS up summary = %q, want Needs Attention", got)
		}
	}
	got := renderUpSummary(stats.Report{TunnelHealthy: true, LatencyMS: 18, UDPMode: "proxy"}, guardian.Status{
		Protection: guardian.ProtectionProtected,
		DNSState:   guardian.DNSManaged,
		DNSManaged: true,
		DNSService: "Wi-Fi",
	})
	if !strings.Contains(got, "Status     Protected") || !strings.Contains(got, "DNS        Wi-Fi managed") {
		t.Fatalf("macOS up summary = %q, want protected managed Wi-Fi DNS", got)
	}
}

func TestMacOSUpLifecycleRemovesOrphanLegacyPlistWithoutMigrating(t *testing.T) {
	var events []string
	client := &recordingGuardianClient{
		events:   &events,
		upStatus: guardian.Status{Desired: guardian.DesiredOn, Phase: guardian.PhaseCommitted, Protection: guardian.ProtectionProtected},
	}
	deps := testMacOSLifecycleDeps(&events, client)
	deps.legacyInstalled = func() bool { return true }
	deps.legacyLoaded = func() (bool, error) {
		events = append(events, "legacy.loaded")
		return false, nil
	}
	deps.migrationRequest = func(context.Context, string) (guardian.MigrationRequest, error) {
		events = append(events, "metadata")
		return guardian.MigrationRequest{}, errors.New("migration request must not be built for an orphan plist")
	}
	result, err := macOSUpLifecycle(context.Background(), "/etc/bx/config.yaml", deps)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"legacy.loaded", "legacy.remove", "guardian.enable", "guardian.ready", "guardian.up", "console.uid", "menu.ensure"}
	if strings.Join(events, "|") != strings.Join(want, "|") {
		t.Fatalf("macOS up events = %#v, want %#v", events, want)
	}
	if result.Status.Protection != guardian.ProtectionProtected || result.MenuWarning != nil {
		t.Fatalf("macOS up result = %+v", result)
	}
	if client.upCalls != 1 || client.migrateCalls != 0 {
		t.Fatalf("Guardian calls = up:%d migrate:%d", client.upCalls, client.migrateCalls)
	}
}

func TestMacOSUpLifecycleStillMigratesWhenLegacyLoaded(t *testing.T) {
	var events []string
	client := &recordingGuardianClient{
		events:        &events,
		migrateStatus: guardian.Status{Protection: guardian.ProtectionStarting},
		statuses:      []guardian.Status{{Desired: guardian.DesiredOn, Phase: guardian.PhaseCommitted, Protection: guardian.ProtectionProtected}},
	}
	deps := testMacOSLifecycleDeps(&events, client)
	deps.legacyInstalled = func() bool { return true }
	deps.legacyLoaded = func() (bool, error) {
		events = append(events, "legacy.loaded")
		return true, nil
	}
	deps.removeLegacyUnit = func() error {
		events = append(events, "legacy.remove")
		return errors.New("removeLegacyUnit must not be called while legacy Core is loaded")
	}
	deps.migrationRequest = func(context.Context, string) (guardian.MigrationRequest, error) {
		events = append(events, "metadata")
		return guardian.MigrationRequest{Gateway: "192.0.2.1", ServerBypass: []string{"198.51.100.10/32"}}, nil
	}
	result, err := macOSUpLifecycle(context.Background(), "/etc/bx/config.yaml", deps)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"legacy.loaded", "metadata", "guardian.enable", "guardian.ready", "guardian.migrate", "guardian.status", "console.uid", "menu.ensure"}
	if strings.Join(events, "|") != strings.Join(want, "|") {
		t.Fatalf("macOS up events = %#v, want %#v", events, want)
	}
	if result.Status.Protection != guardian.ProtectionProtected || result.MenuWarning != nil {
		t.Fatalf("macOS up result = %+v", result)
	}
	if client.upCalls != 0 || client.migrateCalls != 1 {
		t.Fatalf("Guardian calls = up:%d migrate:%d", client.upCalls, client.migrateCalls)
	}
}

func TestMacOSUpLifecycleInstallsGuardianAndLeavesMenuFailureBestEffort(t *testing.T) {
	var events []string
	client := &recordingGuardianClient{
		events:   &events,
		upStatus: guardian.Status{Desired: guardian.DesiredOn, Phase: guardian.PhaseCommitted, Protection: guardian.ProtectionProtected},
	}
	deps := testMacOSLifecycleDeps(&events, client)
	deps.guardianInstalled = func() bool { return false }
	deps.writeGuardianUnit = func(path string) error {
		events = append(events, "guardian.install:"+path)
		return nil
	}
	deps.ensureMenu = func(int) error {
		events = append(events, "menu.ensure")
		return errors.New("menu unavailable")
	}
	result, err := macOSUpLifecycle(context.Background(), "/etc/bx/config.yaml", deps)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"guardian.install:/etc/bx/config.yaml", "legacy.loaded", "guardian.enable", "guardian.ready", "guardian.up", "console.uid", "menu.ensure"}
	if strings.Join(events, "|") != strings.Join(want, "|") {
		t.Fatalf("macOS up events = %#v, want %#v", events, want)
	}
	if result.Status.Protection != guardian.ProtectionProtected || result.MenuWarning == nil {
		t.Fatalf("menu failure changed Core result: %+v", result)
	}
}

func TestMacOSUpLifecycleMetadataFailureLeavesGuardianAndLegacyUntouched(t *testing.T) {
	var events []string
	client := &recordingGuardianClient{events: &events}
	deps := testMacOSLifecycleDeps(&events, client)
	deps.legacyInstalled = func() bool { return true }
	deps.legacyLoaded = func() (bool, error) {
		events = append(events, "legacy.loaded")
		return true, nil
	}
	deps.migrationRequest = func(context.Context, string) (guardian.MigrationRequest, error) {
		events = append(events, "metadata")
		return guardian.MigrationRequest{}, errors.New("invalid handoff")
	}
	if _, err := macOSUpLifecycle(context.Background(), "/etc/bx/config.yaml", deps); err == nil {
		t.Fatal("invalid migration metadata accepted")
	}
	if got, want := strings.Join(events, "|"), "legacy.loaded|metadata"; got != want {
		t.Fatalf("pre-barrier failure events = %q, want %q", got, want)
	}
	if client.upCalls != 0 || client.migrateCalls != 0 {
		t.Fatal("pre-barrier failure reached Guardian mutation")
	}
}

func TestMacOSUpLifecycleFailsWithGuardianLogWhenSocketNeverReady(t *testing.T) {
	var events []string
	client := &recordingGuardianClient{
		upStatus: guardian.Status{Desired: guardian.DesiredOn, Phase: guardian.PhaseCommitted, Protection: guardian.ProtectionProtected},
		events:   &events,
	}
	deps := testMacOSLifecycleDeps(&events, client)
	deps.guardianReady = func(context.Context) bool {
		events = append(events, "guardian.ready")
		return false
	}
	_, err := macOSUpLifecycle(context.Background(), "/etc/bx/config.yaml", deps)
	if err == nil {
		t.Fatal("unready Guardian socket should fail bx up")
	}
	if !strings.Contains(err.Error(), "Guardian 服务未能启动") || !strings.Contains(err.Error(), guardian.SocketPath) {
		t.Fatalf("error missing diagnostic content: %v", err)
	}
	if got, want := strings.Join(events, "|"), "legacy.loaded|guardian.enable|guardian.ready"; got != want {
		t.Fatalf("events = %q, want %q", got, want)
	}
	if client.upCalls != 0 || client.migrateCalls != 0 {
		t.Fatalf("client mutation reached despite unready socket: up=%d migrate=%d", client.upCalls, client.migrateCalls)
	}
}

func TestMacOSDownLifecycleCallsGuardianOnly(t *testing.T) {
	var events []string
	client := &recordingGuardianClient{
		events:     &events,
		downStatus: guardian.Status{Desired: guardian.DesiredOff, Phase: guardian.PhaseIdle, Protection: guardian.ProtectionOff},
	}
	deps := testMacOSLifecycleDeps(&events, client)
	status, err := macOSDownLifecycle(context.Background(), "/etc/bx/config.yaml", deps)
	if err != nil {
		t.Fatal(err)
	}
	// The leading "guardian.ready" is macOSDownLifecycle's own upfront
	// reachability probe (see TestMacOSDownDoesNotRequireGuardianBootstrap):
	// only once that confirms Guardian is already up does it proceed into
	// the normal ensureGuardianOwnership+Down transaction, which itself
	// re-checks readiness after (a no-op) enable.
	want := []string{"guardian.ready", "legacy.loaded", "guardian.enable", "guardian.ready", "guardian.down"}
	if strings.Join(events, "|") != strings.Join(want, "|") {
		t.Fatalf("macOS down events = %#v, want %#v", events, want)
	}
	if status.Protection != guardian.ProtectionOff || client.downCalls != 1 {
		t.Fatalf("macOS down status/calls = %+v/%d", status, client.downCalls)
	}
}

// fakeMacOSLifecycleDeps wraps testMacOSLifecycleDeps with a forceTeardown
// hook and call-counters, for the down-specific escape-hatch tests below:
// down must not require successfully bootstrapping Guardian first (that was
// the production incident — Guardian stuck retrying, `bx down` refusing to
// run because it insisted on starting Guardian before it would stop it).
type fakeMacOSLifecycleDeps struct {
	macOSLifecycleDeps
	events             []string
	client             *recordingGuardianClient
	forceTeardownCount int
	stopCoreCount      int
	clearBarrierCount  int
	desiredOffCount    int
}

func newFakeMacOSLifecycleDeps() *fakeMacOSLifecycleDeps {
	f := &fakeMacOSLifecycleDeps{}
	client := &recordingGuardianClient{
		events:     &f.events,
		downStatus: guardian.Status{Desired: guardian.DesiredOff, Phase: guardian.PhaseIdle, Protection: guardian.ProtectionOff},
	}
	f.client = client
	f.macOSLifecycleDeps = testMacOSLifecycleDeps(&f.events, client)
	f.macOSLifecycleDeps.forceTeardown = func(context.Context) error {
		f.forceTeardownCount++
		f.events = append(f.events, "guardian.forceTeardown")
		return nil
	}
	f.macOSLifecycleDeps.stopCore = func(context.Context) error {
		f.stopCoreCount++
		f.events = append(f.events, "core.shutdown")
		return nil
	}
	f.macOSLifecycleDeps.markDesiredOff = func() error {
		f.desiredOffCount++
		f.events = append(f.events, "desired.off")
		return nil
	}
	f.macOSLifecycleDeps.clearBarrierRoutes = func(context.Context) error {
		f.clearBarrierCount++
		f.events = append(f.events, "barrier.clear")
		return nil
	}
	return f
}

func (f *fakeMacOSLifecycleDeps) trace() string { return strings.Join(f.events, "|") }

func (f *fakeMacOSLifecycleDeps) forcedTeardownCalled() bool { return f.forceTeardownCount > 0 }
func (f *fakeMacOSLifecycleDeps) clientDownCalled() bool     { return f.client.downCalls > 0 }

// TestMacOSDownDoesNotRequireGuardianBootstrap is the regression test for the
// production incident: Guardian was stuck in an infinite recovery retry
// loop, and `bx down` refused to run at all because it insisted on
// successfully starting (bootstrapping) Guardian before it would stop it —
// "shutdown depends on the thing you're shutting down starting up first" is
// exactly backwards, and left the user with no way out short of `bx
// uninstall`. Guardian being unreachable must degrade to a forced teardown
// (stop Guardian, let core's own defer-based restore undo routes/DNS on
// process exit) instead of failing.
func TestMacOSDownDoesNotRequireGuardianBootstrap(t *testing.T) {
	deps := newFakeMacOSLifecycleDeps()
	deps.guardianReady = func(context.Context) bool { return false }
	deps.enableGuardian = func() error { return errors.New("bootstrap Guardian: launchctl failed") }

	if _, err := macOSDownLifecycle(context.Background(), "/etc/bx/config.yaml", deps.macOSLifecycleDeps); err != nil {
		t.Fatalf("Guardian 起不来时 down 仍必须完成拆除,实际返回错误: %v", err)
	}
	if !deps.forcedTeardownCalled() {
		t.Error("Guardian 不可达时应退化为强制拆除(停止 Guardian,由 core 的 defer 还原)")
	}
	if deps.clientDownCalled() {
		t.Error("Guardian 不可达时不应尝试走客户端 Down 事务")
	}
}

// TestMacOSDownUsesGuardianTransactionWhenAvailable proves the fix doesn't
// overcorrect: when Guardian is reachable, down must still go through the
// normal clean transaction rather than always taking the forced shortcut.
func TestMacOSDownUsesGuardianTransactionWhenAvailable(t *testing.T) {
	deps := newFakeMacOSLifecycleDeps()
	deps.guardianReady = func(context.Context) bool { return true }

	if _, err := macOSDownLifecycle(context.Background(), "/etc/bx/config.yaml", deps.macOSLifecycleDeps); err != nil {
		t.Fatal(err)
	}
	if !deps.clientDownCalled() {
		t.Error("Guardian 可达时应走正常的 Down 事务")
	}
	if deps.forcedTeardownCalled() {
		t.Error("Guardian 可达时不应触发强制拆除")
	}
}

// TestMacOSDownForcedTeardownFailureIsActionable proves that when even the
// forced teardown fails, the returned error tells the user what to do next
// instead of surfacing a bare low-level error — the whole point of this
// escape hatch is that the user has a way out.
func TestMacOSDownForcedTeardownFailureIsActionable(t *testing.T) {
	deps := newFakeMacOSLifecycleDeps()
	deps.guardianReady = func(context.Context) bool { return false }
	deps.forceTeardown = func(context.Context) error { return errors.New("launchctl bootout: operation not permitted") }

	_, err := macOSDownLifecycle(context.Background(), "/etc/bx/config.yaml", deps.macOSLifecycleDeps)
	if err == nil {
		t.Fatal("forced teardown failure must not be swallowed")
	}
	if !strings.Contains(err.Error(), "uninstall") {
		t.Fatalf("forced teardown failure not actionable, missing next-step guidance: %v", err)
	}
}

// TestMacOSDownForcedTeardownStopsCoreClearsBarrierAndPersistsOff pins the
// whole forced sequence, in order. Before this, forced teardown only ran
// `launchctl bootout` on Guardian and hoped for the best:
//
//   - the running Core was expected to die from a SIGTERM that happened to
//     reach the whole process group — nothing in the code establishes that
//     process group (no Setpgid/Setsid), Guardian's Shutdown never signals
//     Core, and launchd may follow up with SIGKILL and cut Core's defer-based
//     restore in half. Asking Core to shut itself down over its own control
//     socket (the same cooperative path Guardian's runner uses) removes the
//     guesswork entirely, and must happen BEFORE Guardian is booted out so
//     Core still has a live restore path;
//   - the barrier's /2 reject routes stayed in the kernel forever: bootout
//     destroys the in-memory ownership record, so no later up/down/uninstall
//     can find them, and the machine has zero connectivity while `bx status`
//     happily reports protected. Cleanup must be unconditional;
//   - desired state stayed On, so the next boot's Guardian (RunAtLoad +
//     KeepAlive) restarted Core straight back into the broken state.
func TestMacOSDownForcedTeardownStopsCoreClearsBarrierAndPersistsOff(t *testing.T) {
	deps := newFakeMacOSLifecycleDeps()
	deps.guardianReady = func(context.Context) bool { return false }

	result, err := macOSDownLifecycleDetailed(context.Background(), "/etc/bx/config.yaml", deps.macOSLifecycleDeps)
	if err != nil {
		t.Fatalf("强制拆除应当成功: %v", err)
	}
	if !result.Forced {
		t.Error("Guardian 不可达时应报告走了强制拆除")
	}
	want := "core.shutdown|guardian.forceTeardown|desired.off|barrier.clear"
	if got := deps.trace(); got != want {
		t.Fatalf("强制拆除调用序列 = %q, want %q", got, want)
	}
}

// TestMacOSDownForcedTeardownClearsBarrierEvenWhenEarlierStepsFail is the
// core-value case: the whole point of the escape hatch is that a failure
// anywhere still leaves the user's network usable. If bootout fails and we
// bail out early, the /2 reject routes stay installed and the machine is
// dead — worse than the bug this feature was meant to fix.
func TestMacOSDownForcedTeardownClearsBarrierEvenWhenEarlierStepsFail(t *testing.T) {
	deps := newFakeMacOSLifecycleDeps()
	deps.guardianReady = func(context.Context) bool { return false }
	deps.stopCore = func(context.Context) error { return errors.New("core control socket gone") }
	deps.forceTeardown = func(context.Context) error {
		deps.forceTeardownCount++
		deps.events = append(deps.events, "guardian.forceTeardown")
		return errors.New("launchctl bootout: operation not permitted")
	}

	_, err := macOSDownLifecycleDetailed(context.Background(), "/etc/bx/config.yaml", deps.macOSLifecycleDeps)
	if err == nil {
		t.Fatal("部分步骤失败必须如实报错")
	}
	if deps.clearBarrierCount != 1 {
		t.Fatalf("阻断路由清理必须照常执行(实际 %d 次),否则用户整机断网", deps.clearBarrierCount)
	}
	if deps.desiredOffCount != 1 {
		t.Fatalf("关闭意图必须照常落盘(实际 %d 次),否则重启后保护自己回来", deps.desiredOffCount)
	}
	for _, want := range []string{"uninstall", "route -n delete -net 0.0.0.0/2"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("错误缺少可执行的下一步 %q: %v", want, err)
		}
	}
}

// TestMacOSDownFallsBackToForcedTeardownWhenGuardianDownFails covers the
// reachable-but-permanently-failing Guardian: Manager.Down returns
// errRecoveryIncomplete as its very first statement whenever recoveryBlocked
// is set, which a Guardian restart during a network outage makes permanent.
// The socket answers, so the "unreachable" escape hatch never triggered —
// and by then Down had already installed the block-only barrier, so `bx
// down` failed forever with the machine cut off. Any failure of the clean
// path must fall through to the forced one.
func TestMacOSDownFallsBackToForcedTeardownWhenGuardianDownFails(t *testing.T) {
	deps := newFakeMacOSLifecycleDeps()
	deps.guardianReady = func(context.Context) bool { return true }
	deps.client.downErr = errors.New("guardian recovery incomplete")

	result, err := macOSDownLifecycleDetailed(context.Background(), "/etc/bx/config.yaml", deps.macOSLifecycleDeps)
	if err != nil {
		t.Fatalf("Down 事务失败后必须还有逃生口,实际返回错误: %v", err)
	}
	if !result.Forced || result.Cause == nil {
		t.Fatalf("应报告降级为强制拆除并带上原因: forced=%v cause=%v", result.Forced, result.Cause)
	}
	if !strings.Contains(result.Cause.Error(), "recovery incomplete") {
		t.Errorf("降级原因应保留原始错误: %v", result.Cause)
	}
	if deps.client.downCalls != 1 {
		t.Errorf("仍应先尝试干净事务,Down 调用 = %d", deps.client.downCalls)
	}
	if deps.clearBarrierCount != 1 {
		t.Error("Down 失败时屏障已经装上了,必须清理")
	}
	if deps.forceTeardownCount != 1 || deps.stopCoreCount != 1 || deps.desiredOffCount != 1 {
		t.Errorf("强制拆除步骤不完整: stopCore=%d bootout=%d desiredOff=%d",
			deps.stopCoreCount, deps.forceTeardownCount, deps.desiredOffCount)
	}
}

// TestMacOSDownFallsBackToForcedTeardownWhenOwnershipFails: the legacy-Core
// handoff inside ensureGuardianOwnership needs a default gateway, which a
// network outage removes — the same "stop depends on a precondition that the
// outage just destroyed" trap. Failing there must not strand the user either.
func TestMacOSDownFallsBackToForcedTeardownWhenOwnershipFails(t *testing.T) {
	deps := newFakeMacOSLifecycleDeps()
	deps.guardianReady = func(context.Context) bool { return true }
	deps.legacyLoaded = func() (bool, error) { return true, nil }
	deps.migrationRequest = func(context.Context, string) (guardian.MigrationRequest, error) {
		return guardian.MigrationRequest{}, errors.New("discover migration gateway: default gateway not found")
	}

	result, err := macOSDownLifecycleDetailed(context.Background(), "/etc/bx/config.yaml", deps.macOSLifecycleDeps)
	if err != nil {
		t.Fatalf("接管失败后必须还有逃生口,实际返回错误: %v", err)
	}
	if !result.Forced || deps.clearBarrierCount != 1 {
		t.Fatalf("forced=%v barrier.clear=%d", result.Forced, deps.clearBarrierCount)
	}
}

// TestShutdownRunningCoreAsksTheLiveCoreToStopItself proves the forced path
// no longer relies on an unverified process-group SIGTERM side effect: it
// speaks the same cooperative /v0/shutdown protocol Guardian's own runner
// uses, addressed to the PID the Core itself reported.
func TestShutdownRunningCoreAsksTheLiveCoreToStopItself(t *testing.T) {
	dir, err := os.MkdirTemp("", "bxcore")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "core.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	var shutdownPID int
	mux := http.NewServeMux()
	mux.HandleFunc("/v0/runtime", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(supervisor.RuntimeState{PID: 4242, TunName: "utun7"})
	})
	mux.HandleFunc("/v0/shutdown", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ExpectedPID int `json:"expected_pid"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		shutdownPID = body.ExpectedPID
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})
	server := &http.Server{Handler: mux}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()

	t.Setenv("BX_STATUS_SOCKET", socket)
	if err := shutdownRunningCoreWithin(context.Background(), 50*time.Millisecond); err != nil {
		t.Fatalf("协作关闭 Core 失败: %v", err)
	}
	if shutdownPID != 4242 {
		t.Fatalf("/v0/shutdown expected_pid = %d, want 4242(Core 自报的 PID)", shutdownPID)
	}
}

// 没有可达的 Core(常见:Core 已经死了、只剩孤儿屏障)不是错误——强制拆除必须
// 继续往下走完剩余步骤。
func TestShutdownRunningCoreIsNoopWhenNoCoreIsReachable(t *testing.T) {
	dir, err := os.MkdirTemp("", "bxcore")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	t.Setenv("BX_STATUS_SOCKET", filepath.Join(dir, "absent.sock"))
	if err := shutdownRunningCoreWithin(context.Background(), 50*time.Millisecond); err != nil {
		t.Fatalf("Core 不可达时不应报错: %v", err)
	}
}

// TestDefaultMacOSLifecycleDepsWiresEveryForcedTeardownStep guards the
// production wiring: a nil hook silently skips its step, which is exactly
// how an orphan barrier would come back.
func TestDefaultMacOSLifecycleDepsWiresEveryForcedTeardownStep(t *testing.T) {
	deps := defaultMacOSLifecycleDeps()
	if deps.forceTeardown == nil || deps.stopCore == nil || deps.clearBarrierRoutes == nil || deps.markDesiredOff == nil {
		t.Fatalf("强制拆除挂钩未接全: forceTeardown=%v stopCore=%v clearBarrierRoutes=%v markDesiredOff=%v",
			deps.forceTeardown != nil, deps.stopCore != nil, deps.clearBarrierRoutes != nil, deps.markDesiredOff != nil)
	}
}

func TestMenuLaunchdCommandsUseOnlyCanonicalAndLegacyLabels(t *testing.T) {
	plist := "/Users/alice/Library/LaunchAgents/com.getbx.bx.menu.plist"
	commands := menuLaunchdCommands(501, false, true, plist)
	want := [][]string{
		{"bootout", "gui/501/com.ggshr9.bx.menu"},
		{"bootstrap", "gui/501", plist},
		{"kickstart", "-k", "gui/501/com.getbx.bx.menu"},
	}
	if !reflect.DeepEqual(commands, want) {
		t.Fatalf("menu launchd commands = %#v, want %#v", commands, want)
	}
}

// TestMenuLaunchdCommandsAlwaysBootstrapsSoChangedPlistTakesEffect is the
// regression test for the production hang: launchd only re-reads a plist on
// bootstrap, so a stale-but-loaded canonical label must be booted out and
// rebootstrapped from the current plist rather than just kickstarted, or a
// path change (e.g. UnifiedInstall moving the app) never takes effect and
// `launchctl kickstart -k` blocks forever waiting for a spawn that can never
// succeed.
func TestMenuLaunchdCommandsAlwaysBootstrapsSoChangedPlistTakesEffect(t *testing.T) {
	plist := "/Users/alice/Library/LaunchAgents/com.getbx.bx.menu.plist"

	t.Run("loaded, no legacy", func(t *testing.T) {
		commands := menuLaunchdCommands(501, true, false, plist)
		want := [][]string{
			{"bootout", "gui/501/com.getbx.bx.menu"},
			{"bootstrap", "gui/501", plist},
			{"kickstart", "-k", "gui/501/com.getbx.bx.menu"},
		}
		if !reflect.DeepEqual(commands, want) {
			t.Fatalf("menu launchd commands = %#v, want %#v", commands, want)
		}
	})

	t.Run("not loaded, no legacy", func(t *testing.T) {
		commands := menuLaunchdCommands(501, false, false, plist)
		want := [][]string{
			{"bootstrap", "gui/501", plist},
			{"kickstart", "-k", "gui/501/com.getbx.bx.menu"},
		}
		if !reflect.DeepEqual(commands, want) {
			t.Fatalf("menu launchd commands = %#v, want %#v", commands, want)
		}
	})

	t.Run("loaded plus legacy loaded", func(t *testing.T) {
		commands := menuLaunchdCommands(501, true, true, plist)
		want := [][]string{
			{"bootout", "gui/501/com.ggshr9.bx.menu"},
			{"bootout", "gui/501/com.getbx.bx.menu"},
			{"bootstrap", "gui/501", plist},
			{"kickstart", "-k", "gui/501/com.getbx.bx.menu"},
		}
		if !reflect.DeepEqual(commands, want) {
			t.Fatalf("menu launchd commands = %#v, want %#v", commands, want)
		}
	})

	for _, tc := range []struct {
		name                        string
		currentLoaded, legacyLoaded bool
	}{
		{"not loaded, not legacy", false, false},
		{"loaded, not legacy", true, false},
		{"not loaded, legacy", false, true},
		{"loaded, legacy", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			commands := menuLaunchdCommands(501, tc.currentLoaded, tc.legacyLoaded, plist)
			found := false
			for _, args := range commands {
				if len(args) == 3 && args[0] == "bootstrap" && args[1] == "gui/501" && args[2] == plist {
					found = true
				}
			}
			if !found {
				t.Fatalf("menu launchd commands %#v missing bootstrap of current plist so a changed plist would never take effect", commands)
			}
		})
	}
}

func TestEnsureMacOSMenuRunningWithDepsRemovesLegacyAndVerifiesLabels(t *testing.T) {
	home := t.TempDir()
	currentPlist := filepath.Join(home, "Library", "LaunchAgents", "com.getbx.bx.menu.plist")
	legacyPlist := filepath.Join(home, "Library", "LaunchAgents", "com.ggshr9.bx.menu.plist")
	control := &fakeMenuLaunchdControl{loaded: map[string]bool{
		"gui/501/com.ggshr9.bx.menu": true,
	}}
	var removed []string
	err := ensureMacOSMenuRunningWithDeps(context.Background(), 501, menuBootstrapDeps{
		homeDir: func(int) (string, error) { return home, nil },
		fileExists: func(path string) (bool, error) {
			return path == currentPlist, nil
		},
		remove: func(path string) error {
			removed = append(removed, path)
			return nil
		},
		control: control,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantCalls := []string{
		"bootout gui/501/com.ggshr9.bx.menu",
		"bootstrap gui/501 " + currentPlist,
		"kickstart -k gui/501/com.getbx.bx.menu",
	}
	if !reflect.DeepEqual(control.calls, wantCalls) {
		t.Fatalf("menu launchctl calls = %#v, want %#v", control.calls, wantCalls)
	}
	if !reflect.DeepEqual(removed, []string{legacyPlist}) {
		t.Fatalf("removed menu plists = %#v", removed)
	}
	if !control.loaded["gui/501/com.getbx.bx.menu"] || control.loaded["gui/501/com.ggshr9.bx.menu"] {
		t.Fatalf("final menu labels = %#v", control.loaded)
	}
}

// TestEnsureMacOSMenuRunningWithDepsReloadsStaleLoadedAgent is the
// end-to-end regression test for the production hang: the canonical label
// is already loaded (as it would be after UnifiedInstall moved the app and
// rewrote the plist on disk without reloading launchd), so kickstarting it
// alone would try to respawn a program at a path that no longer exists and
// block forever. It must be booted out and rebootstrapped from the
// (possibly changed) plist on disk, and the legacy plist must be removed
// exactly once, not twice.
func TestEnsureMacOSMenuRunningWithDepsReloadsStaleLoadedAgent(t *testing.T) {
	home := t.TempDir()
	currentPlist := filepath.Join(home, "Library", "LaunchAgents", "com.getbx.bx.menu.plist")
	legacyPlist := filepath.Join(home, "Library", "LaunchAgents", "com.ggshr9.bx.menu.plist")
	control := &fakeMenuLaunchdControl{loaded: map[string]bool{
		"gui/501/com.getbx.bx.menu": true, // stale: loaded, but program on disk moved
	}}
	var removed []string
	err := ensureMacOSMenuRunningWithDeps(context.Background(), 501, menuBootstrapDeps{
		homeDir: func(int) (string, error) { return home, nil },
		fileExists: func(path string) (bool, error) {
			return path == currentPlist, nil
		},
		remove: func(path string) error {
			removed = append(removed, path)
			return nil
		},
		control: control,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantCalls := []string{
		"bootout gui/501/com.getbx.bx.menu",
		"bootstrap gui/501 " + currentPlist,
		"kickstart -k gui/501/com.getbx.bx.menu",
	}
	if !reflect.DeepEqual(control.calls, wantCalls) {
		t.Fatalf("menu launchctl calls = %#v, want %#v", control.calls, wantCalls)
	}
	if !reflect.DeepEqual(removed, []string{legacyPlist}) {
		t.Fatalf("removed menu plists = %#v, want exactly one removal", removed)
	}
	if !control.loaded["gui/501/com.getbx.bx.menu"] || control.loaded["gui/501/com.ggshr9.bx.menu"] {
		t.Fatalf("final menu labels = %#v", control.loaded)
	}
}

// blockingMenuLaunchdControl simulates a launchctl invocation that never
// returns on its own (e.g. `kickstart -k` waiting on a spawn that can never
// succeed) so tests can prove the caller enforces a deadline instead of
// hanging forever.
type blockingMenuLaunchdControl struct {
	loaded map[string]bool
}

func (c *blockingMenuLaunchdControl) Loaded(_ context.Context, label string) (bool, error) {
	return c.loaded[label], nil
}

func (c *blockingMenuLaunchdControl) Run(ctx context.Context, _ ...string) error {
	<-ctx.Done()
	return ctx.Err()
}

func TestEnsureMacOSMenuRunningWithDepsReturnsPromptlyOnTimeout(t *testing.T) {
	home := t.TempDir()
	currentPlist := filepath.Join(home, "Library", "LaunchAgents", "com.getbx.bx.menu.plist")
	control := &blockingMenuLaunchdControl{loaded: map[string]bool{
		"gui/501/com.getbx.bx.menu": true,
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- ensureMacOSMenuRunningWithDeps(ctx, 501, menuBootstrapDeps{
			homeDir:    func(int) (string, error) { return home, nil },
			fileExists: func(path string) (bool, error) { return path == currentPlist, nil },
			remove:     func(string) error { return nil },
			control:    control,
		})
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected timeout error, got nil")
		}
		if !strings.Contains(err.Error(), "bootstrap") || !strings.Contains(err.Error(), "bootout") {
			t.Fatalf("timeout error not actionable: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ensureMacOSMenuRunningWithDeps did not return promptly on context deadline")
	}
}

func TestParseConsoleUIDRejectsRootAndMalformedValues(t *testing.T) {
	if uid, err := parseConsoleUID([]byte("501\n")); err != nil || uid != 501 {
		t.Fatalf("console UID = %d, %v", uid, err)
	}
	for _, value := range [][]byte{[]byte("0\n"), []byte("not-a-uid\n"), nil} {
		if _, err := parseConsoleUID(value); err == nil {
			t.Fatalf("invalid console UID accepted: %q", value)
		}
	}
}

func testMacOSLifecycleDeps(events *[]string, client guardianLifecycleClient) macOSLifecycleDeps {
	return macOSLifecycleDeps{
		guardianInstalled: func() bool { return true },
		writeGuardianUnit: func(string) error {
			*events = append(*events, "guardian.install")
			return nil
		},
		enableGuardian: func() error {
			*events = append(*events, "guardian.enable")
			return nil
		},
		guardianReady: func(context.Context) bool {
			*events = append(*events, "guardian.ready")
			return true
		},
		legacyInstalled: func() bool { return false },
		legacyLoaded: func() (bool, error) {
			*events = append(*events, "legacy.loaded")
			return false, nil
		},
		removeLegacyUnit: func() error {
			*events = append(*events, "legacy.remove")
			return nil
		},
		migrationRequest: func(context.Context, string) (guardian.MigrationRequest, error) {
			*events = append(*events, "metadata")
			return guardian.MigrationRequest{Gateway: "192.0.2.1", ServerBypass: []string{"198.51.100.10/32"}}, nil
		},
		client: client,
		consoleUID: func() (int, error) {
			*events = append(*events, "console.uid")
			return 501, nil
		},
		ensureMenu: func(int) error {
			*events = append(*events, "menu.ensure")
			return nil
		},
		pollInterval: time.Microsecond,
	}
}

type recordingGuardianClient struct {
	events        *[]string
	upStatus      guardian.Status
	downStatus    guardian.Status
	migrateStatus guardian.Status
	statuses      []guardian.Status
	upCalls       int
	downCalls     int
	migrateCalls  int
	downErr       error
}

type fakeMenuLaunchdControl struct {
	loaded map[string]bool
	calls  []string
}

func (f *fakeMenuLaunchdControl) Loaded(_ context.Context, label string) (bool, error) {
	return f.loaded[label], nil
}

func (f *fakeMenuLaunchdControl) Run(_ context.Context, args ...string) error {
	call := strings.Join(args, " ")
	f.calls = append(f.calls, call)
	if len(args) >= 2 && args[0] == "bootout" {
		f.loaded[args[1]] = false
	}
	if len(args) >= 3 && args[0] == "bootstrap" {
		f.loaded[args[1]+"/com.getbx.bx.menu"] = true
	}
	return nil
}

func (c *recordingGuardianClient) Up(context.Context) (guardian.Status, error) {
	c.upCalls++
	*c.events = append(*c.events, "guardian.up")
	return c.upStatus, nil
}

func (c *recordingGuardianClient) Down(context.Context) (guardian.Status, error) {
	c.downCalls++
	*c.events = append(*c.events, "guardian.down")
	if c.downErr != nil {
		return guardian.Status{}, c.downErr
	}
	return c.downStatus, nil
}

func (c *recordingGuardianClient) Migrate(context.Context, guardian.MigrationRequest) (guardian.Status, error) {
	c.migrateCalls++
	*c.events = append(*c.events, "guardian.migrate")
	return c.migrateStatus, nil
}

func (c *recordingGuardianClient) Status(context.Context) (guardian.Status, error) {
	*c.events = append(*c.events, "guardian.status")
	if len(c.statuses) == 0 {
		return guardian.Status{}, errors.New("no status")
	}
	status := c.statuses[0]
	c.statuses = c.statuses[1:]
	return status, nil
}
