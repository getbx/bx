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
	deps.legacyLoaded = func(context.Context) (bool, error) {
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
	deps.legacyLoaded = func(context.Context) (bool, error) {
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
	deps.legacyLoaded = func(context.Context) (bool, error) {
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
	deps.legacyLoaded = func(context.Context) (bool, error) {
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
	// reachability probe (see TestMacOSDownDoesNotRequireGuardianBootstrap).
	// "legacy.loaded" is the path decision: a legacy Core can only be stopped
	// by the forced path, so down must ask (see
	// TestMacOSDownStopsLegacyCoreInsteadOfClaimingSuccess). Only then does
	// it commit the shutdown.
	//
	// 这条序列此前在 legacy.loaded 之后还夹着 guardian.enable|guardian.ready ——
	// cleanGuardianDown 先跑一遍 ensureGuardianOwnership 的痕迹。那两步(以及
	// legacy.loaded 之后原本要做的接管:网关发现、读 runtime、解析 config、DNS
	// 查询)是启动的活,已经搬回 up;留下的只有一次便宜的 launchctl 探查。
	want := []string{"guardian.ready", "legacy.loaded", "guardian.down"}
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
	restoreDNSCount    int
	bootoutLegacyCount int

	// 启动侧的钩子也各带一个计数器。它们存在的理由只有一个:让「停止路径
	// 有没有做启动的活」成为一条可断言的行为事实,而不是靠读源码目视确认。
	// 源码文本匹配在这里是没用的 —— 调用可以换个名字、可以搬进别的函数,
	// 而计数器盯的是它到底跑没跑。
	legacyLoadedCount      int
	migrationRequestCount  int
	removeLegacyUnitCount  int
	writeGuardianUnitCount int
	enableGuardianCount    int
}

func newFakeMacOSLifecycleDeps() *fakeMacOSLifecycleDeps {
	f := &fakeMacOSLifecycleDeps{}
	client := &recordingGuardianClient{
		events:     &f.events,
		downStatus: guardian.Status{Desired: guardian.DesiredOff, Phase: guardian.PhaseIdle, Protection: guardian.ProtectionOff},
		// up 侧要走完 waitGuardianProtected 才会返回,否则它会去轮询
		// Status()(空队列 → 报错),让 up 用例连启动路径都跑不到头。
		upStatus: guardian.Status{Desired: guardian.DesiredOn, Phase: guardian.PhaseCommitted, Protection: guardian.ProtectionProtected},
	}
	f.client = client
	f.macOSLifecycleDeps = testMacOSLifecycleDeps(&f.events, client)
	f.countStartupHooks()
	f.macOSLifecycleDeps.forceTeardown = func(context.Context) error {
		f.forceTeardownCount++
		f.events = append(f.events, "guardian.forceTeardown")
		return nil
	}
	f.macOSLifecycleDeps.bootoutLegacyUnit = func(context.Context) error {
		f.bootoutLegacyCount++
		f.events = append(f.events, "legacy.bootout")
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
	f.macOSLifecycleDeps.restoreSystemDNS = func(context.Context) error {
		f.restoreDNSCount++
		f.events = append(f.events, "dns.restore")
		return nil
	}
	return f
}

// countStartupHooks 把启动侧的五个钩子各包一层计数,行为一字不改(仍然记
// 同样的事件、返回同样的值),只是多记一笔「被调用过」。包一层而不是重写,
// 是为了让计数与 testMacOSLifecycleDeps 的默认行为不会各自漂移。
func (f *fakeMacOSLifecycleDeps) countStartupHooks() {
	legacyLoaded := f.macOSLifecycleDeps.legacyLoaded
	f.macOSLifecycleDeps.legacyLoaded = func(ctx context.Context) (bool, error) {
		f.legacyLoadedCount++
		return legacyLoaded(ctx)
	}
	migrationRequest := f.macOSLifecycleDeps.migrationRequest
	f.macOSLifecycleDeps.migrationRequest = func(ctx context.Context, configPath string) (guardian.MigrationRequest, error) {
		f.migrationRequestCount++
		return migrationRequest(ctx, configPath)
	}
	removeLegacyUnit := f.macOSLifecycleDeps.removeLegacyUnit
	f.macOSLifecycleDeps.removeLegacyUnit = func() error {
		f.removeLegacyUnitCount++
		return removeLegacyUnit()
	}
	writeGuardianUnit := f.macOSLifecycleDeps.writeGuardianUnit
	f.macOSLifecycleDeps.writeGuardianUnit = func(configPath string) error {
		f.writeGuardianUnitCount++
		return writeGuardianUnit(configPath)
	}
	enableGuardian := f.macOSLifecycleDeps.enableGuardian
	f.macOSLifecycleDeps.enableGuardian = func() error {
		f.enableGuardianCount++
		return enableGuardian()
	}
}

// eventIndex reports where an event landed in the recorded sequence, or -1.
func (f *fakeMacOSLifecycleDeps) eventIndex(event string) int {
	for i, got := range f.events {
		if got == event {
			return i
		}
	}
	return -1
}

func (f *fakeMacOSLifecycleDeps) trace() string { return strings.Join(f.events, "|") }

func (f *fakeMacOSLifecycleDeps) forcedTeardownCalled() bool { return f.forceTeardownCount > 0 }
func (f *fakeMacOSLifecycleDeps) clientDownCalled() bool     { return f.client.downCalls > 0 }

// 停止就是停止。发 POST /v1/down 之前不该做网关发现、不该读 Core 的 runtime、
// 不该解析 config、不该做 DNS 查询、不该碰 launchd 的装载与 kickstart。
//
// 这些**曾经**都会跑:cleanGuardianDown 第一步是 ensureGuardianOwnership。后果不是
// 假想的 —— 任何一步报错都会让一次本可以干净完成的停止,升级成拆屏障、还原 DNS、
// 写 desired=off 的重手术。
//
// legacyLoaded 是**唯一**的例外,允许被调用至多一次。理由必须写清楚,否则下一个人
// 会以为这条例外是随手放宽的:
//   - 它只是一次 `launchctl print`,不做网关发现、不解析 config、不查 DNS;
//   - 它决定的是**走哪条路**,本身不是启动的活:legacy Core 只有强制路径停得下来
//     (见 TestMacOSDownStopsLegacyCoreInsteadOfClaimingSuccess);
//   - 它失败也不会把一次无关的故障升级成重手术 —— 失败就去强制路径,而那正是
//     「可能有 legacy Core」时的正确答案(见 TestMacOSDownTakesForcedPathWhenLegacyProbeFails)。
//
// 另外四个钩子仍必须是 0:它们全是启动的活,住在 up 上。
func TestCleanDownDoesNoStartupWork(t *testing.T) {
	f := newFakeMacOSLifecycleDeps()
	if _, err := macOSDownLifecycleDetailed(context.Background(), "/etc/bx/config.yaml", f.macOSLifecycleDeps); err != nil {
		t.Fatal(err)
	}
	if f.forcedTeardownCalled() {
		t.Fatalf("这条用例的前提是走干净路径, trace=%s", f.trace())
	}
	for _, probe := range []struct {
		name  string
		count int
	}{
		{"migrationRequest", f.migrationRequestCount},
		{"removeLegacyUnit", f.removeLegacyUnitCount},
		{"writeGuardianUnit", f.writeGuardianUnitCount},
		{"enableGuardian", f.enableGuardianCount},
	} {
		if probe.count != 0 {
			t.Errorf("干净停止路径上不该调用 %s(实际 %d 次)—— 停止不许依赖与停止无关的工作",
				probe.name, probe.count)
		}
	}
	if f.legacyLoadedCount > 1 {
		t.Errorf("路径判定只需问一次 legacy Core,实际 %d 次", f.legacyLoadedCount)
	}
}

// 这一条是本轮真正要防的事故,而且它比原来那个缺陷更严重。
//
// Guardian 从没启动过 legacy Core(那是旧 launchd unit 起的),所以
// /var/lib/bx/core-process.json 里没有它的记录:ExecCoreRunner.Existing 读不到文件
// 就返回 Process{} —— PID 0。Manager.Down 里 `if process.PID != 0` 那一支于是整个
// 被跳过,Guardian 装上 block-only 屏障、写 desired=off、还原 DNS、**返回成功**,
// 而那个 legacy Core 连同它的 TUN 和路由一动没动。
//
// 用户看到的是 “✅ bx 已停止并取消开机自启。”,而保护其实还开着 —— 一句假话,
// 正是这一期要消灭的那一类。
//
// 只有强制路径停得下它:stopCore 走 supervisor.ShutdownControl 对着 Core 的控制
// socket 说话,跟它是被哪个 launchd unit 拉起来的毫无关系。
func TestMacOSDownStopsLegacyCoreInsteadOfClaimingSuccess(t *testing.T) {
	f := newFakeMacOSLifecycleDeps()
	f.guardianReady = func(context.Context) bool { return true }
	f.legacyLoaded = func(context.Context) (bool, error) { return true, nil }

	result, err := macOSDownLifecycleDetailed(context.Background(), "/etc/bx/config.yaml", f.macOSLifecycleDeps)
	if err != nil {
		t.Fatalf("停止必须完成: %v", err)
	}
	if f.eventIndex("core.shutdown") < 0 {
		t.Errorf("legacy Core 必须被真的请求停下,否则「已停止」是假话, trace=%s", f.trace())
	}
	if !result.Forced {
		t.Errorf("legacy Core 在跑时必须走强制路径(干净路径停不下它), trace=%s", f.trace())
	}
	if f.clientDownCalled() {
		t.Errorf("不该走干净事务:它会报成功而 legacy Core 照旧在跑, trace=%s", f.trace())
	}
}

// **停下来还不够,它得留在停下的状态。**
//
// legacy Core 的 plist 带的是**无条件** KeepAlive(install.go 的 launchd 模板里
// `<key>KeepAlive</key><true/>`,不是 {SuccessfulExit:false} 那种带条件的)。于是
// 只请求 Core 退出的话,它退出 → launchd 立刻(或过了 10s 节流后)把它拉回来 →
// 「已停止」在几秒钟之内重新变成假话。而这条分支的**选中条件本身**就保证了陷阱
// 是armed 的:legacyLoaded==true 的意思就是那个 job 正装在 launchd 里。
//
// 必须先解除 job 再请求退出(反过来就是给 launchd 留一个重启窗口)。
// Guardian 自己的迁移用的正是同一个函数:Manager.Migrate → m.legacy.Stop →
// install.BootoutLegacyCoreUnit。
func TestMacOSDownDisarmsLegacyUnitBeforeAskingCoreToExit(t *testing.T) {
	f := newFakeMacOSLifecycleDeps()
	f.guardianReady = func(context.Context) bool { return true }
	f.legacyLoaded = func(context.Context) (bool, error) { return true, nil }

	if _, err := macOSDownLifecycleDetailed(context.Background(), "/etc/bx/config.yaml", f.macOSLifecycleDeps); err != nil {
		t.Fatalf("停止必须完成: %v", err)
	}
	bootout, shutdown := f.eventIndex("legacy.bootout"), f.eventIndex("core.shutdown")
	if bootout < 0 {
		t.Fatalf("必须解除 legacy 的 launchd job,否则 KeepAlive 会把 Core 拉回来, trace=%s", f.trace())
	}
	if shutdown < 0 {
		t.Fatalf("仍须请求 Core 退出, trace=%s", f.trace())
	}
	if bootout > shutdown {
		t.Errorf("必须先解除 job 再请求退出,否则中间留下一个 launchd 重启窗口, trace=%s", f.trace())
	}
}

// 解除 job 失败不得中断停止的其余步骤 —— 与强制拆除里每一步的约定相同:
// 「停止」不许依赖先成功做成别的事。但它也**不许被静静吞掉**:job 还armed 着
// 意味着 Core 会回来,用户必须知道。
func TestMacOSDownReportsButSurvivesLegacyBootoutFailure(t *testing.T) {
	f := newFakeMacOSLifecycleDeps()
	f.guardianReady = func(context.Context) bool { return true }
	f.legacyLoaded = func(context.Context) (bool, error) { return true, nil }
	f.bootoutLegacyUnit = func(context.Context) error { return errors.New("launchctl bootout: 权限不足") }

	_, err := macOSDownLifecycleDetailed(context.Background(), "/etc/bx/config.yaml", f.macOSLifecycleDeps)
	if err == nil {
		t.Fatal("解除 job 失败必须如实报错 —— 否则用户以为关掉了,而 launchd 正准备把 Core 拉回来")
	}
	if f.stopCoreCount != 1 || f.clearBarrierCount != 1 || f.restoreDNSCount != 1 {
		t.Errorf("解除失败不得中断其余步骤: stopCore=%d barrier=%d dns=%d",
			f.stopCoreCount, f.clearBarrierCount, f.restoreDNSCount)
	}
}

// 探查必须有界。它 fork 两次 `launchctl print`,而且现在**跑在最前面** —— 什么都
// 还没停就先卡住,正是 2026-08-04 那次「71 分钟关不掉保护」的形状。本文件已经为
// restoreSystemDNS 做过同样的事(dnsRestoreTimeout)。
func TestMacOSDownBoundsTheLegacyProbe(t *testing.T) {
	f := newFakeMacOSLifecycleDeps()
	f.guardianReady = func(context.Context) bool { return true }
	var sawDeadline bool
	var remaining time.Duration
	f.legacyLoaded = func(ctx context.Context) (bool, error) {
		deadline, ok := ctx.Deadline()
		sawDeadline = ok
		if ok {
			remaining = time.Until(deadline)
		}
		return false, nil
	}

	if _, err := macOSDownLifecycleDetailed(context.Background(), "/etc/bx/config.yaml", f.macOSLifecycleDeps); err != nil {
		t.Fatal(err)
	}
	if !sawDeadline {
		t.Fatal("legacy 探查必须带有界超时的 context,否则卡住的 launchd 会把停止挂死在第一步")
	}
	if remaining <= 0 || remaining > legacyProbeTimeout {
		t.Fatalf("探查超时窗口不合理: remaining=%v want (0, %v]", remaining, legacyProbeTimeout)
	}
}

// fail-closed 的地板:钩子根本没接线时必须当作「可能有」。
//
// 这条看着平凡,但它是**唯一**盯着那个分支的东西 —— 把它改成 return false 之前
// 整套测试仍然全绿。本仓库对这类地板的先例是 decideCoreScan:判定式抽出来、
// 由单测逐条钉死。
func TestLegacyCoreMayBeRunningAssumesTrueWithoutAProbe(t *testing.T) {
	mayRun, known := legacyCoreMayBeRunning(context.Background(), macOSLifecycleDeps{})
	if !mayRun {
		t.Error("没有探查手段时必须假定可能有 legacy Core —— 「问不出来」不许塌缩成「没有」")
	}
	// known 必须是 false:这决定了解除 job 失败时**不**硬失败 ——
	// 我们其实不知道有没有 legacy unit,一次探查抖动不该让干净机器的 down 整个失败。
	if known {
		t.Error("没有探查手段时不许自称「已知」—— 那会把一次抖动升级成 bx down 硬失败")
	}
}

// 「问不出来」不等于「没有」。探查失败时无法证明没有 legacy Core,安全方向是
// 走那条**万一有就能停下它**的路 —— 与本仓库别处的三态纪律同形。
func TestMacOSDownTakesForcedPathWhenLegacyProbeFails(t *testing.T) {
	f := newFakeMacOSLifecycleDeps()
	f.guardianReady = func(context.Context) bool { return true }
	f.legacyLoaded = func(context.Context) (bool, error) { return false, errors.New("launchctl print: 权限不足") }

	result, err := macOSDownLifecycleDetailed(context.Background(), "/etc/bx/config.yaml", f.macOSLifecycleDeps)
	if err != nil {
		t.Fatalf("探查失败不得让停止本身失败: %v", err)
	}
	if !result.Forced || f.eventIndex("core.shutdown") < 0 {
		t.Errorf("无法证明没有 legacy Core 时必须走强制路径, forced=%v trace=%s", result.Forced, f.trace())
	}
}

// 反向:迁移没有丢,它留在 up 上 —— 那本来就是它该在的地方。
func TestUpStillDoesTheStartupWork(t *testing.T) {
	f := newFakeMacOSLifecycleDeps()
	if _, err := macOSUpLifecycle(context.Background(), "/etc/bx/config.yaml", f.macOSLifecycleDeps); err != nil {
		t.Fatal(err)
	}
	if f.legacyLoadedCount == 0 {
		t.Error("up 必须仍然检查 legacy Core —— 把它从 down 上摘掉不等于把它删掉")
	}
}

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
//     KeepAlive) restarted Core straight back into the broken state — and,
//     worse, so did the *live* Guardian the moment Core stopped (see
//     TestMacOSDownForcedTeardownPersistsDesiredOffBeforeStoppingCore);
//   - the system DNS Guardian pointed at 127.0.0.1 was never restored by
//     anyone (see
//     TestMacOSDownForcedTeardownRestoresSystemDNSAfterGuardianIsGone).
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
	want := "desired.off|core.shutdown|guardian.forceTeardown|barrier.clear|dns.restore|desired.off"
	if got := deps.trace(); got != want {
		t.Fatalf("强制拆除调用序列 = %q, want %q", got, want)
	}
}

// TestMacOSDownForcedTeardownPersistsDesiredOffBeforeStoppingCore pins the
// step that makes the whole forced sequence deterministic instead of a race.
//
// The dangerous case is a Guardian that is *alive* but whose Down transaction
// fails permanently (recoveryBlocked). Stopping Core first wakes Guardian's
// monitor goroutine, which calls handleUnexpectedExit; that function reads the
// desired state straight off the store (manager.go: `desired, err :=
// m.store.LoadDesired()`) and, while it still reads On, reinstalls the eight
// blocking reject routes via installBarrierForRecovery and restarts Core via
// startCoreLocked. We would then boot Guardian out and clear the barrier — but
// only if we won the race, and the loser's prize is exactly the orphan barrier
// (or a brand-new unmanaged Core) this whole feature exists to prevent.
//
// Persisting DesiredOff *first* removes the race: LoadDesired sees Off and
// handleUnexpectedExit returns immediately (`if desired != DesiredOn {
// m.current = Process{}; return }`) without touching routes or Core.
func TestMacOSDownForcedTeardownPersistsDesiredOffBeforeStoppingCore(t *testing.T) {
	deps := newFakeMacOSLifecycleDeps()
	deps.guardianReady = func(context.Context) bool { return true }
	deps.client.downErr = errors.New("guardian recovery incomplete")

	if _, err := macOSDownLifecycleDetailed(context.Background(), "/etc/bx/config.yaml", deps.macOSLifecycleDeps); err != nil {
		t.Fatalf("强制拆除应当成功: %v", err)
	}
	desiredOff, stopCore := deps.eventIndex("desired.off"), deps.eventIndex("core.shutdown")
	if desiredOff < 0 || stopCore < 0 {
		t.Fatalf("强制拆除缺步骤: %q", deps.trace())
	}
	if desiredOff > stopCore {
		t.Fatalf("必须先落 DesiredOff 再停 Core,否则活着的 Guardian 会重装屏障并重启 Core: %q", deps.trace())
	}
	// Guardian can no longer overwrite it once booted out, so the final
	// write makes Off the last word on disk rather than a hopeful one.
	if deps.desiredOffCount != 2 {
		t.Errorf("DesiredOff 应在停 Core 前落一次、bootout 后再幂等落一次,实际 %d 次", deps.desiredOffCount)
	}
}

// TestMacOSDownForcedTeardownRestoresSystemDNSAfterGuardianIsGone covers the
// symptom the user actually reported: "网页打不开".
//
// On macOS the system resolver is taken over by *Guardian*, not Core
// (guardian/dns.go → install.EnableDNSContext → `networksetup -setdnsservers
// <svc> 127.0.0.1`), and only Manager.Down's restoreDNS ever undoes it —
// Daemon.Shutdown does not, and internal/supervisor does not even import
// internal/install, so Core cannot: it is merely the process listening on
// 127.0.0.1:53. A forced teardown that skips this leaves every DNS query
// pointed at a listener that just exited, so the machine still cannot browse
// even though its routes are perfectly clean.
//
// Ordering is load-bearing: restoring before the bootout would race Guardian
// re-taking DNS (ensureDNS runs on its recovery paths), so this must come
// after Guardian is gone.
func TestMacOSDownForcedTeardownRestoresSystemDNSAfterGuardianIsGone(t *testing.T) {
	deps := newFakeMacOSLifecycleDeps()
	deps.guardianReady = func(context.Context) bool { return false }

	if _, err := macOSDownLifecycleDetailed(context.Background(), "/etc/bx/config.yaml", deps.macOSLifecycleDeps); err != nil {
		t.Fatalf("强制拆除应当成功: %v", err)
	}
	if deps.restoreDNSCount != 1 {
		t.Fatalf("系统 DNS 必须还原(实际 %d 次),否则 DNS 仍指向已退出的 127.0.0.1:53 监听者", deps.restoreDNSCount)
	}
	bootout, restoreDNS := deps.eventIndex("guardian.forceTeardown"), deps.eventIndex("dns.restore")
	if bootout < 0 || restoreDNS < 0 || restoreDNS < bootout {
		t.Fatalf("还原 DNS 必须在停掉 Guardian 之后(它才是接管者,活着会重新接管): %q", deps.trace())
	}
}

// TestMacOSDownForcedTeardownDNSFailureIsActionable: a DNS restore that fails
// must neither abort the remaining steps nor leave the user guessing — the
// manual equivalent (`sudo bx dns off`) has to be in the error.
func TestMacOSDownForcedTeardownDNSFailureIsActionable(t *testing.T) {
	deps := newFakeMacOSLifecycleDeps()
	deps.guardianReady = func(context.Context) bool { return false }
	deps.restoreSystemDNS = func(context.Context) error {
		deps.restoreDNSCount++
		deps.events = append(deps.events, "dns.restore")
		return errors.New("networksetup: 权限不足")
	}

	_, err := macOSDownLifecycleDetailed(context.Background(), "/etc/bx/config.yaml", deps.macOSLifecycleDeps)
	if err == nil {
		t.Fatal("DNS 还原失败必须如实报错")
	}
	if deps.clearBarrierCount != 1 {
		t.Errorf("DNS 还原失败不得中断后续步骤,阻断路由清理实际 %d 次", deps.clearBarrierCount)
	}
	if !strings.Contains(err.Error(), "bx dns off") {
		t.Errorf("错误缺少可执行的下一步 %q: %v", "bx dns off", err)
	}
}

// TestMacOSDownForcedTeardownClearsBarrierBeforeRestoringDNS pins the reorder
// this fix makes: clearing the barrier's blocking routes is what actually
// gets the user back online, so it must not sit behind the DNS restore
// step's several unbounded external commands (networksetup x2, dscacheutil,
// killall — see dnsRestoreTimeout). The two steps do not depend on each
// other: route deletion touches no DNS state, and restoring DNS touches no
// routes.
func TestMacOSDownForcedTeardownClearsBarrierBeforeRestoringDNS(t *testing.T) {
	deps := newFakeMacOSLifecycleDeps()
	deps.guardianReady = func(context.Context) bool { return false }

	if _, err := macOSDownLifecycleDetailed(context.Background(), "/etc/bx/config.yaml", deps.macOSLifecycleDeps); err != nil {
		t.Fatalf("强制拆除应当成功: %v", err)
	}
	clearBarrier, restoreDNS := deps.eventIndex("barrier.clear"), deps.eventIndex("dns.restore")
	if clearBarrier < 0 || restoreDNS < 0 {
		t.Fatalf("强制拆除缺步骤: %q", deps.trace())
	}
	if clearBarrier > restoreDNS {
		t.Fatalf("清屏障(恢复联网的关键步)必须先于 DNS 还原(可能因无超时的外部命令卡住): %q", deps.trace())
	}
}

// TestMacOSDownForcedTeardownDNSRestoreHasBoundedDeadline proves the DNS
// restore hook is invoked with a context carrying a deadline bounded by
// dnsRestoreTimeout, rather than the caller's raw context — which, via
// app.Run(os.Args) in main.go, is context.Background() and never expires on
// its own. Without this, a stuck `networksetup` call (the failure mode that
// motivated this whole escape hatch) hangs `bx down` forever.
func TestMacOSDownForcedTeardownDNSRestoreHasBoundedDeadline(t *testing.T) {
	deps := newFakeMacOSLifecycleDeps()
	deps.guardianReady = func(context.Context) bool { return false }
	var sawDeadline bool
	var remaining time.Duration
	deps.restoreSystemDNS = func(ctx context.Context) error {
		deps.restoreDNSCount++
		deps.events = append(deps.events, "dns.restore")
		deadline, ok := ctx.Deadline()
		sawDeadline = ok
		if ok {
			remaining = time.Until(deadline)
		}
		return nil
	}

	if _, err := macOSDownLifecycleDetailed(context.Background(), "/etc/bx/config.yaml", deps.macOSLifecycleDeps); err != nil {
		t.Fatalf("强制拆除应当成功: %v", err)
	}
	if !sawDeadline {
		t.Fatal("DNS 还原必须带有界超时的 context,否则可能被无超时的外部命令挂住而永不返回")
	}
	if remaining <= 0 || remaining > dnsRestoreTimeout {
		t.Fatalf("DNS 还原超时窗口不合理: remaining=%v want (0, %v]", remaining, dnsRestoreTimeout)
	}
}

// TestRunWithTimeoutBoundsHangingCall proves runWithTimeout actually cancels
// a hook that blocks on ctx.Done(), rather than merely decorating the
// context and hoping the callee respects it — fast to run (short custom
// wait) unlike a full end-to-end test at the production dnsRestoreTimeout.
func TestRunWithTimeoutBoundsHangingCall(t *testing.T) {
	start := time.Now()
	err := runWithTimeout(context.Background(), 50*time.Millisecond, func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("挂住的调用超时后必须返回错误")
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("runWithTimeout 未能及时截断挂住的调用,耗时 %v", elapsed)
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
	if deps.desiredOffCount != 2 {
		t.Fatalf("关闭意图必须照常落盘(停 Core 前 + bootout 后各一次,实际 %d 次),否则重启后保护自己回来", deps.desiredOffCount)
	}
	if deps.restoreDNSCount != 1 {
		t.Fatalf("系统 DNS 必须照常还原(实际 %d 次),否则用户仍然网页打不开", deps.restoreDNSCount)
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
	if deps.forceTeardownCount != 1 || deps.stopCoreCount != 1 || deps.desiredOffCount != 2 || deps.restoreDNSCount != 1 {
		t.Errorf("强制拆除步骤不完整: stopCore=%d bootout=%d desiredOff=%d dnsRestore=%d",
			deps.stopCoreCount, deps.forceTeardownCount, deps.desiredOffCount, deps.restoreDNSCount)
	}
}

// TestMacOSDownFallsBackToForcedTeardownWhenLegacyCoreLoaded:同一个 fixture,
// 同一个结论,**理由换了,而且换成了更好的那个**。
//
// 场景:有一个 legacy Core 在跑,而一次断网正好抹掉了默认网关,于是 legacy 接管
// 所需的网关发现必然失败。
//
// 旧理由:停止路径会去做接管,接管失败 → 干净路径失败 → 退化为强制拆除。也就是说
// 结论对,但对得很偶然 —— 它是被一个「停止根本不该做的动作」的失败推过去的,而那个
// 动作换成任何别的失败方式(读 runtime 超时、DNS 查询挂住)结果都一样重手术。
//
// 新理由:停止路径不再做接管,但它仍然会问一句「有没有 legacy Core 在跑」。有 ⇒
// 走强制路径,因为**只有强制路径停得下它**(干净路径会报成功而它照旧在跑,见
// TestMacOSDownStopsLegacyCoreInsteadOfClaimingSuccess)。网关发现失败与这个判断
// 完全无关 —— 这条用例里的 migrationRequest 现在一次都不会被调用。
func TestMacOSDownFallsBackToForcedTeardownWhenLegacyCoreLoaded(t *testing.T) {
	deps := newFakeMacOSLifecycleDeps()
	deps.guardianReady = func(context.Context) bool { return true }
	deps.legacyLoaded = func(context.Context) (bool, error) { return true, nil }
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
	// 新旧理由的分水岭:接管本身已经不在这条路上了。
	if deps.migrationRequestCount != 0 {
		t.Errorf("停止路径不得再做 legacy 接管(实际 %d 次)", deps.migrationRequestCount)
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
	if deps.forceTeardown == nil || deps.stopCore == nil || deps.clearBarrierRoutes == nil ||
		deps.markDesiredOff == nil || deps.restoreSystemDNS == nil {
		t.Fatalf("强制拆除挂钩未接全: forceTeardown=%v stopCore=%v clearBarrierRoutes=%v markDesiredOff=%v restoreSystemDNS=%v",
			deps.forceTeardown != nil, deps.stopCore != nil, deps.clearBarrierRoutes != nil,
			deps.markDesiredOff != nil, deps.restoreSystemDNS != nil)
	}
}

func TestMenuLaunchdCommandsUseOnlyCanonicalAndLegacyLabels(t *testing.T) {
	plist := "/Users/alice/Library/LaunchAgents/com.getbx.bx.menu.plist"
	commands := menuLaunchdCommands(501, false, true, true, plist)
	want := [][]string{
		{"bootout", "gui/501/com.ggshr9.bx.menu"},
		{"asuser", "501", "launchctl", "bootstrap", "gui/501", plist},
		{"kickstart", "-k", "gui/501/com.getbx.bx.menu"},
	}
	if !reflect.DeepEqual(commands, want) {
		t.Fatalf("menu launchd commands = %#v, want %#v", commands, want)
	}
}

// TestMenuLaunchdCommandsBootstrapOnForcedReloadSoChangedPlistTakesEffect is the
// regression test for the production hang: launchd only re-reads a plist on
// bootstrap, so a stale-but-loaded canonical label must be booted out and
// rebootstrapped from the current plist rather than just kickstarted, or a
// path change (e.g. UnifiedInstall moving the app) never takes effect and
// `launchctl kickstart -k` blocks forever waiting for a spawn that can never
// succeed.
func TestMenuLaunchdCommandsBootstrapOnForcedReloadSoChangedPlistTakesEffect(t *testing.T) {
	plist := "/Users/alice/Library/LaunchAgents/com.getbx.bx.menu.plist"

	t.Run("loaded, no legacy", func(t *testing.T) {
		commands := menuLaunchdCommands(501, true, false, true, plist)
		want := [][]string{
			{"bootout", "gui/501/com.getbx.bx.menu"},
			{"asuser", "501", "launchctl", "bootstrap", "gui/501", plist},
			{"kickstart", "-k", "gui/501/com.getbx.bx.menu"},
		}
		if !reflect.DeepEqual(commands, want) {
			t.Fatalf("menu launchd commands = %#v, want %#v", commands, want)
		}
	})

	t.Run("not loaded, no legacy", func(t *testing.T) {
		commands := menuLaunchdCommands(501, false, false, true, plist)
		want := [][]string{
			{"asuser", "501", "launchctl", "bootstrap", "gui/501", plist},
			{"kickstart", "-k", "gui/501/com.getbx.bx.menu"},
		}
		if !reflect.DeepEqual(commands, want) {
			t.Fatalf("menu launchd commands = %#v, want %#v", commands, want)
		}
	})

	t.Run("loaded plus legacy loaded", func(t *testing.T) {
		commands := menuLaunchdCommands(501, true, true, true, plist)
		want := [][]string{
			{"bootout", "gui/501/com.ggshr9.bx.menu"},
			{"bootout", "gui/501/com.getbx.bx.menu"},
			{"asuser", "501", "launchctl", "bootstrap", "gui/501", plist},
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
			commands := menuLaunchdCommands(501, tc.currentLoaded, tc.legacyLoaded, true, plist)
			want := menuBootstrapCommand(501, plist)
			found := false
			for _, args := range commands {
				if reflect.DeepEqual(args, want) {
					found = true
				}
			}
			if !found {
				t.Fatalf("menu launchd commands %#v missing bootstrap of current plist so a changed plist would never take effect", commands)
			}
		})
	}
}

// TestMenuBootstrapUsesAsuserWhenRoot is the regression test for the
// production failure: `bx up` runs as root, and root bootstrapping
// gui/<uid> directly returns EIO(5) ("Bootstrap failed: 5: Input/output
// error") because root carries no audit session token for the console
// user's GUI domain. `launchctl asuser <uid>` re-execs launchctl inside
// that user's session first, which is what actually succeeded when the
// identical bootstrap was run manually as the console user on real
// hardware.
func TestMenuBootstrapUsesAsuserWhenRoot(t *testing.T) {
	args := menuBootstrapCommand(501, "/Users/test/Library/LaunchAgents/com.getbx.bx.menu.plist")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "asuser 501") {
		t.Errorf("必须经 launchctl asuser 进入用户上下文,实际:%s", joined)
	}
	if !strings.Contains(joined, "bootstrap gui/501") {
		t.Errorf("仍应 bootstrap 到用户 GUI 域,实际:%s", joined)
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
	err := ensureMacOSMenuRunningWithDeps(context.Background(), 501, true, menuBootstrapDeps{
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
		"asuser 501 launchctl bootstrap gui/501 " + currentPlist,
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
	err := ensureMacOSMenuRunningWithDeps(context.Background(), 501, true, menuBootstrapDeps{
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
		"asuser 501 launchctl bootstrap gui/501 " + currentPlist,
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
		done <- ensureMacOSMenuRunningWithDeps(ctx, 501, true, menuBootstrapDeps{
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
		legacyLoaded: func(context.Context) (bool, error) {
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

	downForUpgradeCalls int
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
	// bootstrap is now issued via `launchctl asuser <uid> launchctl bootstrap
	// <domain> <plist>` (see menuBootstrapCommand), so the domain moved from
	// args[1] to args[4].
	if len(args) >= 6 && args[0] == "asuser" && args[2] == "launchctl" && args[3] == "bootstrap" {
		f.loaded[args[4]+"/com.getbx.bx.menu"] = true
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

// DownForUpgrade 单独记事件:调用方用哪一个,决定 Guardian 会不会把升级欠条
// 当作陈旧记录销掉,而两者的返回值一模一样 —— 弄错了不会有任何编译或运行期
// 提示,只会在一次中途失败的升级之后留下一台永远不再被保护的机器。
func (c *recordingGuardianClient) DownForUpgrade(ctx context.Context) (guardian.Status, error) {
	c.downForUpgradeCalls++
	*c.events = append(*c.events, "guardian.down.upgrade")
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

// 共存提示(severity=warn)不得把总状态写成 Needs Attention。
//
// 真机 2026-08-06:隧道健康 322ms、Guardian Protected、观测层 divergence 为空,
// 但每次 sudo bx up 都显示 "Status Needs Attention",只因 Tailscale 在跑而产生
// 一条 advisory。告警本身已经单独占一行显示,把头条状态一并降级是重复的,而且
// 会让完全正常的保护看起来像出了故障——用户据此判断要不要排查。
func TestUpSummaryKeepsProtectedForAdvisoryWarnings(t *testing.T) {
	report := stats.Report{
		TunnelHealthy: true, LatencyMS: 322, UDPMode: "proxy",
		Warnings: []stats.Warning{{
			Name: "tailscale", Severity: "warn",
			Detail: "macOS VPN service active: Tailscale",
		}},
	}
	got := renderUpSummary(report, protectedGuardianStatus())
	if !strings.Contains(got, "Status     Protected") {
		t.Errorf("仅有 advisory 时总状态必须仍是 Protected,实际 =\n%s", got)
	}
	if !strings.Contains(got, "tailscale") && !strings.Contains(got, "Tailscale") {
		t.Errorf("告警本身仍须显示,不能因为不降级就不提,实际 =\n%s", got)
	}
}

// severity=error 的告警仍必须拉低总状态——这条区分才是降级逻辑存在的意义。
func TestUpSummaryDowngradesForErrorSeverityWarnings(t *testing.T) {
	report := stats.Report{
		TunnelHealthy: true, LatencyMS: 18, UDPMode: "proxy",
		Warnings: []stats.Warning{{Name: "leak", Severity: "error", Detail: "流量绕过隧道"}},
	}
	got := renderUpSummary(report, protectedGuardianStatus())
	if !strings.Contains(got, "Needs Attention") {
		t.Errorf("error 级告警必须拉低总状态,实际 =\n%s", got)
	}
}

// protectedGuardianStatus 构造一个在 darwin 上也真正算 protected 的 Guardian 状态:
// normalizedGuardianProtectionState 在 macOS 上额外要求 DNS 接管证据,缺了它会被
// 正确降级成 needs_attention,那样就测不到告警严重度这条分支了。
func protectedGuardianStatus() guardian.Status {
	return guardian.Status{
		Protection: guardian.ProtectionProtected,
		DNSState:   guardian.DNSManaged,
		DNSManaged: true,
		DNSService: "Wi-Fi",
	}
}

// bx up 不得把已经在跑的菜单栏杀掉重启。
//
// 真机反馈 2026-08-06:「start 之后会有短暂图标消失,用户不知道发生了什么,
// 然后一会图标回归」。原因是 menuLaunchdCommands 无条件 bootout + bootstrap
// + kickstart -k,而 ensureMacOSMenuRunning 挂在 bx up 的生命周期钩子上——
// 每次启动保护都把菜单栏进程杀一遍。
//
// plist 变更只发生在安装/更新之后,那条路径显式要求强制重载;bx up 只需要
// 「确保它在跑」。
func TestMenuLaunchdCommandsDoNotRestartAlreadyRunningMenu(t *testing.T) {
	plist := "/Users/alice/Library/LaunchAgents/com.getbx.bx.menu.plist"

	got := menuLaunchdCommands(501, true, false, false, plist)
	for _, args := range got {
		if len(args) > 0 && args[0] == "bootout" {
			t.Errorf("已加载的菜单栏不得被 bootout(会让图标消失):%v", got)
		}
		if len(args) > 1 && args[0] == "kickstart" && args[1] == "-k" {
			t.Errorf("不得用 kickstart -k 强制重启已在跑的菜单栏:%v", got)
		}
	}

	// 但仍要确保它真的在跑:崩溃后干净退出时 KeepAlive{SuccessfulExit:false}
	// 不会拉起来,菜单栏会一直缺席。不带 -k 的 kickstart 跑着就是 no-op。
	var kickstarts int
	for _, args := range got {
		if len(args) > 0 && args[0] == "kickstart" {
			kickstarts++
		}
	}
	if kickstarts == 0 {
		t.Errorf("仍须 kickstart 以修复「已加载但没在跑」,实际 = %v", got)
	}

	// 强制重载(安装/更新后)照旧完整重启,否则改过的 plist 不会生效。
	forced := menuLaunchdCommands(501, true, false, true, plist)
	var sawBootout, sawForcedKickstart bool
	for _, args := range forced {
		if len(args) > 0 && args[0] == "bootout" {
			sawBootout = true
		}
		if len(args) > 1 && args[0] == "kickstart" && args[1] == "-k" {
			sawForcedKickstart = true
		}
	}
	if !sawBootout || !sawForcedKickstart {
		t.Errorf("强制重载必须 bootout + kickstart -k,否则改过的 plist 不生效,实际 = %v", forced)
	}

	// 未加载时无论是否强制都必须 bootstrap,否则菜单栏根本起不来。
	notLoaded := menuLaunchdCommands(501, false, false, false, plist)
	var sawBootstrap bool
	for _, args := range notLoaded {
		if isMenuBootstrapCommand(args) {
			sawBootstrap = true
		}
	}
	if !sawBootstrap {
		t.Errorf("未加载时必须 bootstrap,实际 = %v", notLoaded)
	}
}

// 探查失败时,解除 legacy job 失败**不得**让 bx down 整个失败。
//
// 这条分支是探查失败也会进的,而 bootoutLegacyCoreWithControl 内部会重跑那个刚刚
// 失败的 launchctl 查询 —— 于是一台**根本没有 legacy unit** 的干净机器,遇上一次
// launchctl 抖动就会让 bx down 非零退出、报告被吞掉。那正是「停止不许依赖别的先
// 成功」本身:停止的成败被一次与停止无关的查询绑架了。
func TestLegacyDisarmFailureIsNotFatalWhenTheProbeItselfFailed(t *testing.T) {
	f := newFakeMacOSLifecycleDeps()
	f.guardianReady = func(context.Context) bool { return true }
	f.legacyLoaded = func(context.Context) (bool, error) { return false, errors.New("launchctl 抖了一下") }
	f.bootoutLegacyUnit = func(context.Context) error { return errors.New("bootout 也失败") }

	result, err := macOSDownLifecycleDetailed(context.Background(), "/etc/bx/config.yaml", f.macOSLifecycleDeps)
	if err != nil {
		t.Fatalf("探查失败时,解除 job 的失败不该让停止本身失败: %v", err)
	}
	if !result.Forced {
		t.Errorf("仍应如实报告走了强制路径, trace=%s", f.trace())
	}
}

// 反过来:**确知** job 加载着时,解除失败就是硬失败 —— job 还armed,Core 一定回来,
// 此时说「已停止」是假话。
func TestLegacyDisarmFailureIsFatalWhenTheUnitIsKnownLoaded(t *testing.T) {
	f := newFakeMacOSLifecycleDeps()
	f.guardianReady = func(context.Context) bool { return true }
	f.legacyLoaded = func(context.Context) (bool, error) { return true, nil }
	f.bootoutLegacyUnit = func(context.Context) error { return errors.New("bootout 失败") }

	result, err := macOSDownLifecycleDetailed(context.Background(), "/etc/bx/config.yaml", f.macOSLifecycleDeps)
	if err == nil {
		t.Fatal("确知 job 加载着而解除失败时必须报错 —— KeepAlive 会把 Core 拉回来,「已停止」是假话")
	}
	// **出错时返回的 result 也不能是零值。** 零值经 downReportLines 渲染出来的是
	// "✅ bx 已停止并取消开机自启。" —— 这一期要杀的那句原话,离被打印只差一个
	// 忽略 err 的调用方(upgraderun.go 那条路正是拿结果去渲染的)。
	if !result.Forced {
		t.Error("出错路径返回的结果必须仍标记 Forced —— 零值会被渲染成干净成功")
	}
	if stdout, _ := downReportLines(result); strings.Contains(strings.Join(stdout, "\n"), "✅") {
		t.Errorf("出错路径的结果渲染成了干净成功:\n%s", strings.Join(stdout, "\n"))
	}
}
