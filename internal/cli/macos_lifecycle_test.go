//go:build darwin

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/guardian"
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
	want := []string{"legacy.loaded", "metadata", "guardian.enable", "guardian.migrate", "guardian.status", "console.uid", "menu.ensure"}
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
	want := []string{"guardian.install:/etc/bx/config.yaml", "legacy.loaded", "guardian.enable", "guardian.up", "console.uid", "menu.ensure"}
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
	want := []string{"legacy.loaded", "guardian.enable", "guardian.down"}
	if strings.Join(events, "|") != strings.Join(want, "|") {
		t.Fatalf("macOS down events = %#v, want %#v", events, want)
	}
	if status.Protection != guardian.ProtectionOff || client.downCalls != 1 {
		t.Fatalf("macOS down status/calls = %+v/%d", status, client.downCalls)
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
	commands = menuLaunchdCommands(501, true, false, plist)
	want = [][]string{{"kickstart", "-k", "gui/501/com.getbx.bx.menu"}}
	if !reflect.DeepEqual(commands, want) {
		t.Fatalf("idempotent menu commands = %#v, want %#v", commands, want)
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
		legacyInstalled: func() bool { return false },
		legacyLoaded: func() (bool, error) {
			*events = append(*events, "legacy.loaded")
			return false, nil
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
