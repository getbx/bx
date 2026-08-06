package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/getbx/bx/internal/blink"
	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/guardian"
	"github.com/getbx/bx/internal/install"
	"github.com/getbx/bx/internal/observe"
	"github.com/getbx/bx/internal/stats"
	"github.com/urfave/cli/v2"
)

func TestAppHasVersion(t *testing.T) {
	app := New()
	if strings.TrimSpace(app.Version) == "" {
		t.Fatal("app version should not be empty")
	}
	if !appHasCommand(app, "logs") {
		t.Fatal("app should expose bx logs")
	}
	logs := findAppCommand(app, "logs")
	if !commandHasFlag(logs, "archive") || !commandHasFlag(logs, "dir") || !commandHasFlag(logs, "json") {
		t.Fatal("logs should expose --archive, --dir and --json")
	}
	if !appHasCommand(app, "dns") {
		t.Fatal("app should expose bx dns")
	}
	if !appHasCommand(app, "realtime") {
		t.Fatal("app should expose bx realtime")
	}
	if !appHasCommand(app, "preset") {
		t.Fatal("app should expose bx preset")
	}
	preset := findAppCommand(app, "preset")
	if !commandHasSubcommand(preset, "ls") || !commandHasSubcommand(preset, "show") || !commandHasSubcommand(preset, "apply") {
		t.Fatalf("preset subcommands = %+v, want ls/show/apply", preset.Subcommands)
	}
	if !appHasCommand(app, "webrtc-check") {
		t.Fatal("app should expose bx webrtc-check")
	}
	webrtc := findAppCommand(app, "webrtc-check")
	if !commandHasFlag(webrtc, "browser") || !commandHasFlag(webrtc, "expected-ip") {
		t.Fatal("webrtc-check should expose --browser and --expected-ip")
	}
	if !appHasCommand(app, "leak-check") {
		t.Fatal("app should expose bx leak-check")
	}
	leak := findAppCommand(app, "leak-check")
	if !commandHasFlag(leak, "json") || !commandHasFlag(leak, "browser") || !commandHasFlag(leak, "expected-ip") || !commandHasFlag(leak, "network") || !commandHasFlag(leak, "network-timeout") {
		t.Fatal("leak-check should expose --json, --browser, --expected-ip, --network and --network-timeout")
	}
	observe := findAppCommand(app, "observe")
	if observe == nil || !commandHasFlag(observe, "json") || !commandHasFlag(observe, "duration") || !commandHasFlag(observe, "interval") {
		t.Fatal("observe should expose --json, --duration and --interval")
	}
	status := findAppCommand(app, "status")
	if !commandHasFlag(status, "json") {
		t.Fatal("status should expose --json")
	}
	if !appHasCommand(app, "reconnect") {
		t.Fatal("app should expose bx reconnect")
	}
	if reconnect := findAppCommand(app, "reconnect"); !commandHasFlag(reconnect, "json") {
		t.Fatal("reconnect should expose --json")
	}
	inspect := findAppCommand(app, "inspect")
	if inspect == nil || !commandHasFlag(inspect, "json") {
		t.Fatal("inspect should expose --json")
	}
	realtime := findAppCommand(app, "realtime")
	if !commandHasSubcommand(realtime, "status") || !commandHasSubcommand(realtime, "on") || !commandHasSubcommand(realtime, "off") {
		t.Fatalf("realtime subcommands = %+v, want status/on/off", realtime.Subcommands)
	}
	if commandHasFlag(realtime, "no-restart") {
		t.Fatal("realtime must not offer a service-restart path")
	}
	if !subcommandHidden(realtime, "on") || !subcommandHidden(realtime, "off") {
		t.Fatal("realtime on/off should stay hidden from the normal help surface")
	}
}

func TestMacMenuReconnectDoesNotCycleProtection(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "apps", "macos", "BxMenu", "Sources", "BxMenu", "main.swift"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	if !strings.Contains(text, "menu.addAction(\"Troubleshoot: Reconnect\"") {
		t.Fatal("macOS menu should expose Reconnect only as troubleshooting")
	}
	if !strings.Contains(text, "func reconnectBx()") {
		t.Fatal("macOS menu should implement reconnect action")
	}
	if !strings.Contains(text, "guardianClient.requestRecovery()") {
		t.Fatal("macOS reconnect should submit directly to Guardian")
	}
	if strings.Contains(text, "runPrivileged(\"'\\(bxPath)' reconnect\")") {
		t.Fatal("macOS reconnect must not invoke the CLI through privileged AppleScript")
	}
	if strings.Contains(text, "'\\(bxPath)' down &&") {
		t.Fatal("macOS reconnect must not cycle bx down && bx up")
	}
}

func TestReconnectSubmitsOncePollsToSuccessAndPrintsHumanProgress(t *testing.T) {
	var output bytes.Buffer
	client := &scriptedRecoveryClient{
		submitted: guardian.RecoverySnapshot{
			ID: "recovery-7", State: "accepted", Stage: "queued", Reason: "manual",
		},
		current: []guardian.RecoverySnapshot{
			{ID: "recovery-7", State: "running", Stage: "transport_health", Reason: "manual"},
			{ID: "recovery-7", State: "succeeded", Stage: "succeeded", Reason: "manual"},
		},
	}
	var waits []time.Duration
	err := reconnectWithDependencies(context.Background(), false, reconnectDependencies{
		client: client,
		output: &output,
		wait: func(_ context.Context, delay time.Duration) error {
			waits = append(waits, delay)
			return nil
		},
		legacyReconnect: func(context.Context) error {
			t.Fatal("successful Guardian recovery must not use legacy Core reconnect")
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if client.submitCalls != 1 {
		t.Fatalf("Guardian submit calls = %d, want 1", client.submitCalls)
	}
	if got, want := waits, []time.Duration{250 * time.Millisecond, 500 * time.Millisecond}; !reflect.DeepEqual(got, want) {
		t.Fatalf("poll waits = %v, want %v", got, want)
	}
	if got, want := output.String(), "• Protection  Reconnecting\n✓ Protection  Reconnected\n"; got != want {
		t.Fatalf("human output = %q, want %q", got, want)
	}
}

func TestReconnectJSONReturnsFinalRecoverySnapshot(t *testing.T) {
	var output bytes.Buffer
	client := &scriptedRecoveryClient{
		submitted: guardian.RecoverySnapshot{
			ID: "recovery-8", State: "succeeded", Stage: "succeeded", Reason: "manual", Attempt: 2,
		},
	}
	if err := reconnectWithDependencies(context.Background(), true, reconnectDependencies{
		client: client,
		output: &output,
		wait:   func(context.Context, time.Duration) error { return nil },
	}); err != nil {
		t.Fatal(err)
	}
	var got guardian.RecoverySnapshot
	if err := json.Unmarshal(output.Bytes(), &got); err != nil {
		t.Fatalf("decode reconnect JSON: %v\n%s", err, output.String())
	}
	if got.ID != "recovery-8" || got.State != "succeeded" || got.Attempt != 2 {
		t.Fatalf("final snapshot = %+v", got)
	}
}

func TestReconnectReportsStableGuardianFailureWithoutLegacyFallback(t *testing.T) {
	client := &scriptedRecoveryClient{
		submitted: guardian.RecoverySnapshot{
			ID: "recovery-9", State: "accepted", Stage: "queued", Reason: "manual",
		},
		current: []guardian.RecoverySnapshot{{
			ID: "recovery-9", State: "failed", Stage: "observe", Reason: "manual",
			ErrorCode: "network_unavailable", Detail: "secret underlay detail",
		}},
	}
	legacyCalls := 0
	err := reconnectWithDependencies(context.Background(), false, reconnectDependencies{
		client: client,
		output: io.Discard,
		wait:   func(context.Context, time.Duration) error { return nil },
		legacyReconnect: func(context.Context) error {
			legacyCalls++
			return nil
		},
	})
	if err == nil || err.Error() != "recovery failed: network_unavailable" {
		t.Fatalf("reconnect error = %v, want stable recovery code", err)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("reconnect error leaked Guardian detail: %v", err)
	}
	if legacyCalls != 0 {
		t.Fatalf("legacy Core reconnect calls = %d, want 0", legacyCalls)
	}
}

func TestReconnectFallsBackOnlyForTypedGuardianUnavailable(t *testing.T) {
	tests := []struct {
		name       string
		requestErr error
		wantLegacy int
		wantError  string
	}{
		{
			name:       "typed unavailable",
			requestErr: &guardian.UnavailableError{Err: errors.New("missing socket")},
			wantLegacy: 1,
		},
		{
			name:       "Guardian business failure",
			requestErr: errors.New("Guardian /v1/recoveries returned 500"),
			wantError:  "Guardian /v1/recoveries returned 500",
		},
		{
			name:       "possibly accepted POST",
			requestErr: &guardian.AmbiguousRecoveryError{Err: io.ErrUnexpectedEOF},
			wantError:  "Guardian recovery request may have been accepted: unexpected EOF",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			legacyCalls := 0
			err := reconnectWithDependencies(context.Background(), false, reconnectDependencies{
				client: &scriptedRecoveryClient{requestErr: tt.requestErr},
				output: io.Discard,
				wait:   func(context.Context, time.Duration) error { return nil },
				legacyReconnect: func(context.Context) error {
					legacyCalls++
					return nil
				},
			})
			if tt.wantError == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || err.Error() != tt.wantError {
				t.Fatalf("reconnect error = %v, want %q", err, tt.wantError)
			}
			if legacyCalls != tt.wantLegacy {
				t.Fatalf("legacy Core reconnect calls = %d, want %d", legacyCalls, tt.wantLegacy)
			}
		})
	}
}

func TestReconnectLegacyFallbackSuccessPrintsHumanProgress(t *testing.T) {
	var output bytes.Buffer
	legacyCalls := 0
	err := reconnectWithDependencies(context.Background(), false, reconnectDependencies{
		client: &scriptedRecoveryClient{
			requestErr: &guardian.UnavailableError{Err: errors.New("missing Guardian socket")},
		},
		output: &output,
		legacyReconnect: func(context.Context) error {
			legacyCalls++
			return nil
		},
	})
	if err != nil {
		t.Fatalf("legacy reconnect exit error = %v, want nil", err)
	}
	if legacyCalls != 1 {
		t.Fatalf("legacy reconnect calls = %d, want 1", legacyCalls)
	}
	if got, want := output.String(), "• Protection  Reconnecting\n✓ Protection  Reconnected\n"; got != want {
		t.Fatalf("legacy human stdout = %q, want %q", got, want)
	}
}

func TestReconnectLegacyFallbackSuccessPrintsStableJSONOnly(t *testing.T) {
	var output bytes.Buffer
	err := reconnectWithDependencies(context.Background(), true, reconnectDependencies{
		client: &scriptedRecoveryClient{
			requestErr: &guardian.UnavailableError{Err: errors.New("missing Guardian socket")},
		},
		output:          &output,
		legacyReconnect: func(context.Context) error { return nil },
	})
	if err != nil {
		t.Fatalf("legacy JSON reconnect exit error = %v, want nil", err)
	}
	const want = "{\n  \"state\": \"succeeded\",\n  \"stage\": \"legacy_core\",\n  \"reason\": \"manual\",\n  \"attempt\": 1\n}\n"
	if got := output.String(); got != want {
		t.Fatalf("legacy JSON stdout = %q, want %q", got, want)
	}
	var decoded map[string]any
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatalf("legacy stdout is not valid JSON: %v", err)
	}
}

func TestReconnectObservationTimeoutKeepsRecoveryRunning(t *testing.T) {
	client := &scriptedRecoveryClient{submitted: guardian.RecoverySnapshot{
		ID: "recovery-10", State: "accepted", Stage: "queued", Reason: "manual",
	}}
	err := reconnectWithDependencies(context.Background(), false, reconnectDependencies{
		client: client,
		output: io.Discard,
		wait: func(context.Context, time.Duration) error {
			return context.DeadlineExceeded
		},
	})
	if err == nil || err.Error() != "recovery recovery-10 is still running: context deadline exceeded" {
		t.Fatalf("timeout error = %v", err)
	}
	if strings.Contains(strings.ToLower(err.Error()), "failed") {
		t.Fatalf("timeout must not claim recovery failure: %v", err)
	}
}

type scriptedRecoveryClient struct {
	submitted   guardian.RecoverySnapshot
	current     []guardian.RecoverySnapshot
	requestErr  error
	currentErr  error
	submitCalls int
	currentCall int
}

func (c *scriptedRecoveryClient) RequestRecovery(context.Context, guardian.RecoveryRequest) (guardian.RecoverySnapshot, error) {
	c.submitCalls++
	return c.submitted, c.requestErr
}

func (c *scriptedRecoveryClient) CurrentRecovery(context.Context) (guardian.RecoverySnapshot, error) {
	if c.currentErr != nil {
		return guardian.RecoverySnapshot{}, c.currentErr
	}
	if c.currentCall >= len(c.current) {
		return c.submitted, nil
	}
	snapshot := c.current[c.currentCall]
	c.currentCall++
	return snapshot, nil
}

func TestMacMenuQuitBxStopsProtectionThenQuitsMenu(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "apps", "macos", "BxMenu", "Sources", "BxMenu", "main.swift"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	if !strings.Contains(text, "menu.addAction(quitBxActionTitle") {
		t.Fatal("macOS menu should expose an explicit Quit bx action")
	}
	if !strings.Contains(text, "func quitBx()") || !strings.Contains(text, "'\\(bxPath)' down") {
		t.Fatal("Quit bx should use the safe bx down path")
	}
	if !strings.Contains(text, "NSApp.terminate(nil)") {
		t.Fatal("Quit bx should close the menu only after bx stops")
	}
}

func TestMacMenuUsesGuardianInsteadOfCLIReconnectSupport(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "apps", "macos", "BxMenu", "Sources", "BxMenu", "main.swift"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	if !strings.Contains(text, "private let guardianClient = GuardianClient()") {
		t.Fatal("macOS menu should own a fixed Guardian client")
	}
	if strings.Contains(text, "func cliSupportsSafeReconnect()") || strings.Contains(text, "func runtimeSupportsSafeReconnect()") {
		t.Fatal("macOS menu must not probe the legacy CLI/Core reconnect path")
	}
}

// The menu must read DNS state only from the authoritative `bx status --json`
// report. A second source (shelling out to `bx dns status` and parsing its
// lines) can disagree with Guardian and paint the shield green while DNS is
// unmanaged, which is exactly the leak this guard prevents from returning.
func TestMacMenuDerivesDNSFromStatusJSONOnly(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "apps", "macos", "BxMenu", "Sources", "BxMenu", "main.swift"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	if strings.Contains(text, "loadDNSStatus") {
		t.Fatal("macOS menu must not keep a second DNS status source (loadDNSStatus)")
	}
	if strings.Contains(text, `runBx(["dns", "status"])`) {
		t.Fatal("macOS menu must not shell out to `bx dns status` for menu state")
	}
	if !strings.Contains(text, "dnsPresentation(") {
		t.Fatal("macOS menu should derive its DNS verdict from dnsPresentation")
	}
	for _, key := range []string{`case dnsState = "dns_state"`, `case dnsManaged = "dns_managed"`, `case dnsService = "dns_service"`} {
		if !strings.Contains(text, key) {
			t.Fatalf("macOS menu should decode the authoritative status field: %s", key)
		}
	}
}

// The CLI and the macOS menu both render DNS state to a human, in two languages
// that cannot share code. They must not drift into saying the same state two
// different ways, so pin the wording on both sides. The raw enum stays in
// `bx status --json` for machines.
func TestGuardianDNSLabelMatchesMenuWording(t *testing.T) {
	cases := []struct {
		state   guardian.DNSState
		service string
		want    string
	}{
		{guardian.DNSManaged, "Wi-Fi", "Wi-Fi managed"},
		{guardian.DNSManaged, "", "Managed"},
		{guardian.DNSUnmanaged, "", "Not managed"},
		{guardian.DNSUnmanaged, "Wi-Fi", "Not managed"},
		{guardian.DNSUnknown, "", "Status unavailable"},
		{"", "", "Status unavailable"},
	}
	for _, tc := range cases {
		if got := guardianDNSLabel(tc.state, tc.service); got != tc.want {
			t.Errorf("guardianDNSLabel(%q, %q) = %q, want %q", tc.state, tc.service, got, tc.want)
		}
	}

	source, err := os.ReadFile(filepath.Join("..", "..", "apps", "macos", "BxMenu", "Sources", "BxMenu", "StatusPresentation.swift"))
	if err != nil {
		t.Fatal(err)
	}
	swift := string(source)
	for _, literal := range []string{`"\(service) managed"`, `"Managed"`, `"Not managed"`, `"Status unavailable"`} {
		if !strings.Contains(swift, literal) {
			t.Errorf("StatusPresentation.swift should render DNS with the same wording as the CLI: %s", literal)
		}
	}
}

// updateIcon lets a live recovery snapshot override the state-derived indicator,
// so a warning verdict must drop any snapshot that would paint the shield green
// — otherwise the icon shows green while the tunnel is unhealthy or DNS is
// unmanaged. loadState is an AppKit method that shells out, so this wiring has no
// unit test; pin it at the source level instead.
func TestMacMenuWarningsDropGreenRecoverySnapshot(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "apps", "macos", "BxMenu", "Sources", "BxMenu", "main.swift"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	branches := map[string]string{
		"tunnel unhealthy": "recoverySnapshot = recoverySnapshotSurvivingWarning(recoverySnapshot)\n" +
			`            return .warning("Tunnel unhealthy", version: version)`,
		"DNS not managed": "recoverySnapshot = recoverySnapshotSurvivingWarning(recoverySnapshot)\n" +
			`            return .warning(dns.menuWarning ?? "DNS status unavailable", version: version)`,
	}
	for name, snippet := range branches {
		if !strings.Contains(text, snippet) {
			t.Fatalf("the %s warning branch must filter the recovery snapshot before returning", name)
		}
	}
}

func TestMacMenuAndReadmeDescribeAutomaticSafeNetworkRecovery(t *testing.T) {
	menu, err := os.ReadFile(filepath.Join("..", "..", "apps", "macos", "BxMenu", "Sources", "BxMenu", "main.swift"))
	if err != nil {
		t.Fatal(err)
	}
	readme, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(menu), "Automatically recovers safely after network changes") {
		t.Fatal("macOS menu should describe automatic safe recovery after network changes")
	}
	for _, want := range []string{"网络变化后自动安全恢复", "`bx reconnect`", "troubleshooting", "绝不回落直连"} {
		if !strings.Contains(string(readme), want) {
			t.Fatalf("README missing recovery guidance %q", want)
		}
	}
}

func TestDarwinTestkitNetworkTransitionCheckIsDryRunAndUserGated(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "scripts", "darwin-testkit.sh"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, want := range []string{
		"--network-transition-check",
		"--acknowledge-physical-change",
		"before-status.json",
		"after-status.json",
		"user-sequence.txt",
		"NETWORK-CHANGED",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("network transition testkit missing %q", want)
		}
	}
	start := strings.Index(text, "run_network_transition_check() {")
	if start < 0 {
		t.Fatal("network transition check function is missing")
	}
	end := strings.Index(text[start:], "\n}\n\n")
	if end < 0 {
		t.Fatal("network transition check function end is missing")
	}
	body := text[start : start+end]
	for _, want := range []string{
		`if [[ "$EXECUTE" != "1" ]]`,
		`if [[ "$ACKNOWLEDGE_PHYSICAL_CHANGE" != "1" ]]`,
		`"$BX" status --json`,
		`"protection_state"[[:space:]]*:[[:space:]]*"protected"`,
		`"tunnel_healthy"[[:space:]]*:[[:space:]]*true`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("network transition check body missing %q", want)
		}
	}
	for _, forbidden := range []string{"networksetup -setairport", "airport -z", `"$BX" reconnect`} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("network transition check actively changes the network with %q", forbidden)
		}
	}
}

func TestDarwinTestkitNetworkTransitionModeTerminatesBeforeLegacyHarness(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "scripts", "darwin-testkit.sh"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	dispatch := strings.Index(text, `if [[ "$NETWORK_TRANSITION_CHECK" == "1" ]]`)
	legacy := strings.Index(text, "PLAN_ARGS=(darwin-plan")
	if dispatch < 0 || legacy < 0 || dispatch >= legacy {
		t.Fatalf("network transition dispatch=%d legacy harness=%d", dispatch, legacy)
	}
	between := text[dispatch:legacy]
	if !strings.Contains(between, "run_network_transition_check\n  exit 0") {
		t.Fatalf("network transition mode can fall through into legacy harness:\n%s", between)
	}
}

func TestDarwinTestkitNetworkTransitionLogDirectoryIsUniqueAndPrivate(t *testing.T) {
	tmp := t.TempDir()
	first := runDarwinTransitionFixture(t, tmp)
	second := runDarwinTransitionFixture(t, tmp)
	firstDir := transitionLogDir(t, first)
	secondDir := transitionLogDir(t, second)
	t.Cleanup(func() {
		_ = os.RemoveAll(firstDir)
		_ = os.RemoveAll(secondDir)
	})
	if firstDir == secondDir {
		t.Fatalf("default transition log directory was reused: %s", firstDir)
	}
	for _, dir := range []string{firstDir, secondDir} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o700 {
			t.Fatalf("transition log directory mode = %04o, want 0700", got)
		}
	}
}

func TestDarwinTestkitRejectsExistingOrSymlinkLogDirectory(t *testing.T) {
	tmp := t.TempDir()
	existing := filepath.Join(tmp, "existing")
	if err := os.Mkdir(existing, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(tmp, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(tmp, "symlink")
	if err := os.Symlink(target, symlink); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{existing, symlink} {
		cmd := exec.Command("bash", filepath.Join("..", "..", "scripts", "darwin-testkit.sh"),
			"--network-transition-check", "--log-dir", path)
		output, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("existing log path %q was accepted:\n%s", path, output)
		}
		if !strings.Contains(string(output), "log directory must not already exist") {
			t.Fatalf("existing log path error = %q", output)
		}
	}
}

func TestDarwinTestkitRejectsSymlinkedLogDirectoryParent(t *testing.T) {
	root := trustedRepoTempDir(t)
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	linkedParent := filepath.Join(root, "linked-parent")
	if err := os.Symlink(target, linkedParent); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("bash", filepath.Join("..", "..", "scripts", "darwin-testkit.sh"),
		"--network-transition-check", "--log-dir", filepath.Join(linkedParent, "logs"))
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("symlinked log parent was accepted:\n%s", output)
	}
	if !strings.Contains(string(output), "log directory parent must not contain symbolic links") {
		t.Fatalf("symlinked log parent error = %q", output)
	}
}

func TestDarwinTestkitRejectsGroupOrWorldWritableLogDirectoryParent(t *testing.T) {
	tests := []struct {
		name string
		mode os.FileMode
	}{
		{name: "group writable", mode: 0o770},
		{name: "world writable", mode: 0o702},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := trustedRepoTempDir(t)
			untrusted := filepath.Join(root, "untrusted")
			if err := os.Mkdir(untrusted, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(untrusted, tt.mode); err != nil {
				t.Fatal(err)
			}

			cmd := exec.Command("bash", filepath.Join("..", "..", "scripts", "darwin-testkit.sh"),
				"--network-transition-check", "--log-dir", filepath.Join(untrusted, "logs"))
			output, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("%s log parent was accepted:\n%s", tt.name, output)
			}
			if !strings.Contains(string(output), "log directory parent must not be group/other writable") {
				t.Fatalf("%s log parent error = %q", tt.name, output)
			}
		})
	}
}

func TestDarwinTestkitAcceptsPrivateUserOwnedLogDirectoryParent(t *testing.T) {
	root := trustedRepoTempDir(t)
	logDir := filepath.Join(root, "logs")
	cmd := exec.Command("bash", filepath.Join("..", "..", "scripts", "darwin-testkit.sh"),
		"--network-transition-check", "--log-dir", logDir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("private user-owned log parent was rejected: %v\n%s", err, output)
	}
	info, err := os.Stat(logDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("custom transition log directory mode = %04o, want 0700", got)
	}
}

func TestDarwinTestkitExecutedTransitionFixtureRunsOnlyStatusSnapshots(t *testing.T) {
	tmp := trustedRepoTempDir(t)
	fakeBX := filepath.Join(tmp, "bx-fixture")
	fixture := `#!/usr/bin/env bash
set -eu
printf '%s\n' "$*" >>"$0.args"
count=0
if [[ -f "$0.count" ]]; then count="$(cat "$0.count")"; fi
count=$((count + 1))
printf '%s\n' "$count" >"$0.count"
generation=wifi-a
if [[ "$count" -gt 1 ]]; then generation=wifi-b; fi
printf '{"protection_state":"protected","tunnel_healthy":true,"network_generation":"%s"}\n' "$generation"
`
	if err := os.WriteFile(fakeBX, []byte(fixture), 0o700); err != nil {
		t.Fatal(err)
	}
	logDir := filepath.Join(tmp, "transition-logs")
	cmd := exec.Command("bash", filepath.Join("..", "..", "scripts", "darwin-testkit.sh"),
		"--network-transition-check", "--execute", "--acknowledge-physical-change",
		"--bx", fakeBX, "--log-dir", logDir)
	cmd.Stdin = strings.NewReader("NETWORK-CHANGED\n")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fixture transition: %v\n%s", err, output)
	}
	args, err := os.ReadFile(fakeBX + ".args")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(args), "status --json\nstatus --json\n"; got != want {
		t.Fatalf("fixture bx calls = %q, want %q", got, want)
	}
	if strings.Contains(string(output), "darwin-plan") {
		t.Fatalf("transition fixture reached legacy harness:\n%s", output)
	}
}

func trustedRepoTempDir(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(wd, ".darwin-testkit-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func runDarwinTransitionFixture(t *testing.T, tmp string) string {
	t.Helper()
	cmd := exec.Command("bash", filepath.Join("..", "..", "scripts", "darwin-testkit.sh"), "--network-transition-check")
	cmd.Env = append(os.Environ(), "TMPDIR="+tmp)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("network transition dry-run: %v\n%s", err, output)
	}
	return string(output)
}

func transitionLogDir(t *testing.T, output string) string {
	t.Helper()
	const marker = "Dry-run complete. Logs: "
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, marker) {
			return strings.TrimPrefix(line, marker)
		}
	}
	t.Fatalf("dry-run output omitted log directory:\n%s", output)
	return ""
}

func TestMacMenuUsesSingleCompactStatusIcon(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "apps", "macos", "BxMenu", "Sources", "BxMenu", "main.swift"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	if !strings.Contains(text, "compactStatusImage(for:") {
		t.Fatal("macOS menu should render its shield and state indicator as one compact icon")
	}
	if strings.Contains(text, "button.attributedTitle = statusDotTitle") {
		t.Fatal("macOS menu should not consume menu-bar space with a separate status-dot title")
	}
}

func TestMacOSMenuInstallerUsesSingularLaunchdLabels(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "scripts", "install-macos-menu.sh"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, want := range []string{
		`launchctl bootout "$DOMAIN/$LEGACY_BUNDLE_ID"`,
		`rm -f "$LEGACY_AGENT_DST"`,
		`launchctl bootstrap "$DOMAIN" "$AGENT_DST"`,
		`launchctl kickstart -k "$DOMAIN/$BUNDLE_ID"`,
		`verify_single_agent`,
		`launchctl print "$DOMAIN/$BUNDLE_ID"`,
		`launchctl print "$DOMAIN/$LEGACY_BUNDLE_ID"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("macOS menu installer missing %q", want)
		}
	}
	for _, forbidden := range []string{"pgrep", `launchctl bootout "$DOMAIN" "$LEGACY_AGENT_DST"`} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("macOS menu installer contains non-singular check %q", forbidden)
		}
	}
}

func TestAppHidesLegacyAndDeveloperCommands(t *testing.T) {
	app := New()
	for _, name := range []string{"restart", "blink", "run", "debug-tun", "darwin-plan", "router-plan"} {
		command := findAppCommand(app, name)
		if command == nil || !command.Hidden {
			t.Fatalf("%s should stay available but hidden from normal help: %+v", name, command)
		}
	}
}

func TestBuildExecStart(t *testing.T) {
	// Linux: 标准路径格式无引号
	got := buildExecStartForGOOS("linux", "/usr/local/bin/bx", "/etc/bx/config.yaml")
	want := "/usr/local/bin/bx run -c /etc/bx/config.yaml"
	if got != want {
		t.Fatalf("Linux ExecStart 应跑 run, got %q", got)
	}

	// darwin legacy: Guardian 可执行文件在 /usr/local/bin/bx
	got = buildExecStartWith("darwin", "/usr/local/bin/bx", "/etc/bx/config.yaml", "/usr/local/bin/bx")
	want = "/usr/local/bin/bx guardian --config /etc/bx/config.yaml --listen-dns 127.0.0.1:53"
	if got != want {
		t.Fatalf("darwin legacy ExecStart, got %q", got)
	}

	// darwin unified runtime: Guardian 可执行文件在统一 runtime 目录(含空格)
	got = buildExecStartWith("darwin", "/usr/local/bin/bx", "/etc/bx/config.yaml", "/Library/Application Support/bx/runtime/current/bx")
	want = "/Library/Application Support/bx/runtime/current/bx guardian --config /etc/bx/config.yaml --listen-dns 127.0.0.1:53"
	if got != want {
		t.Fatalf("darwin unified runtime ExecStart (with spaces in path), got %q", got)
	}

	// Windows: 含空格路径必须加引号,否则服务 BinaryPathName 拆分会崩。
	got = buildExecStartForGOOS("windows", `C:\Program Files\bx\bx.exe`, `C:\ProgramData\bx\config.yaml`)
	want = `"C:\Program Files\bx\bx.exe" run -c "C:\ProgramData\bx\config.yaml"`
	if got != want {
		t.Fatalf("windows ExecStart 应对含空格路径加引号, got %q", got)
	}
}

func TestBlinkRoundTripThroughCLI(t *testing.T) {
	link := "brook://server?server=1.2.3.4%3A9999&password=pw"
	enc := blink.Encode(link)
	dec, err := blink.Decode(enc)
	if err != nil || dec != link {
		t.Fatalf("round-trip 失败: %q err=%v", dec, err)
	}
}

func TestNormalizeClientLinkAcceptsRawBrook(t *testing.T) {
	raw := "brook://wssserver?wssserver=wss%3A%2F%2Fvps.example.com%3A443&username&password=pw"
	link, configLink, err := normalizeClientLink(raw)
	if err != nil {
		t.Fatal(err)
	}
	if link != raw {
		t.Fatalf("link = %q, want raw brook link", link)
	}
	if !strings.HasPrefix(configLink, "bx://") {
		t.Fatalf("config link should be normalized to bx://, got %q", configLink)
	}
	decoded, err := blink.Decode(configLink)
	if err != nil {
		t.Fatal(err)
	}
	if decoded != raw {
		t.Fatalf("decoded config link = %q, want %q", decoded, raw)
	}
}

func TestNormalizeClientLinkAcceptsEncodedBX(t *testing.T) {
	raw := "brook://server?server=1.2.3.4%3A9999&password=pw"
	encoded := blink.Encode(raw)
	link, configLink, err := normalizeClientLink(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if link != raw || configLink != encoded {
		t.Fatalf("link/config = %q/%q, want %q/%q", link, configLink, raw, encoded)
	}
}

func TestNormalizeClientLinkAcceptsVless(t *testing.T) {
	raw := "vless://be625ca6@1.2.3.4:9998?security=reality&pbk=PUB&sid=ab12&sni=www.apple.com&flow=xtls-rprx-vision&fp=chrome"
	link, configLink, err := normalizeClientLink(raw)
	if err != nil {
		t.Fatalf("vless 链接应被接受: %v", err)
	}
	if link != raw {
		t.Fatalf("link = %q, want raw vless link", link)
	}
	if !strings.HasPrefix(configLink, "bx://") {
		t.Fatalf("config link 应换壳成 bx://, got %q", configLink)
	}
	decoded, err := blink.Decode(configLink)
	if err != nil {
		t.Fatal(err)
	}
	if decoded != raw {
		t.Fatalf("decoded config link = %q, want %q", decoded, raw)
	}
}

func TestBXServerLink(t *testing.T) {
	link, err := bxServerLink("example.com", serverConfig{Listen: ":9999", Password: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := blink.Decode(link)
	if err != nil {
		t.Fatal(err)
	}
	want := "brook://server?server=example.com%3A9999&password=pw"
	if raw != want {
		t.Fatalf("raw link = %q, want %q", raw, want)
	}
}

func TestBXServerLinkRejectsHostWithPort(t *testing.T) {
	if _, err := bxServerLink("example.com:8443", serverConfig{Listen: ":9999", Password: "pw"}); err == nil {
		t.Fatal("host 带端口应报错,端口应来自 listen")
	}
}

func TestServerFirewallHint(t *testing.T) {
	got := serverFirewallHint(":9998")
	for _, want := range []string{"TCP 9998", "sudo ufw allow 9998/tcp"} {
		if !strings.Contains(got, want) {
			t.Fatalf("firewall hint = %q, want contains %q", got, want)
		}
	}
	if got := serverFirewallHint("bad-listen"); got != "" {
		t.Fatalf("bad listen should not produce hint, got %q", got)
	}
}

func TestOpenUFWRejectsBadListen(t *testing.T) {
	if err := openUFW("bad-listen"); err == nil {
		t.Fatal("bad listen should fail")
	}
}

func TestDoctorHelpers(t *testing.T) {
	if got := boolStatus(true); got != "ok" {
		t.Fatalf("boolStatus(true)=%q", got)
	}
	if got := boolStatus(false); got != "fail" {
		t.Fatalf("boolStatus(false)=%q", got)
	}
	if got := redactLink("bx://secret"); got != "bx://<redacted>" {
		t.Fatalf("redact bx link = %q", got)
	}
	if got := redactLink("brook://server?password=pw"); got != "internal-link:<redacted>" {
		t.Fatalf("redact internal link = %q", got)
	}
	if got := shareDoctorStatus("active", "listening"); got != "ok" {
		t.Fatalf("shareDoctorStatus active/listening = %q", got)
	}
	if got := shareDoctorStatus("inactive", "listening"); got != "warn" {
		t.Fatalf("shareDoctorStatus inactive/listening = %q", got)
	}
	if got := hintForState("inactive", "sudo bx up", "bx logs"); got != "sudo bx up; bx logs" {
		t.Fatalf("hintForState inactive = %q", got)
	}
}

func TestClientDoctorJSONReport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.yaml")
	rep := collectClientDoctor(path, "example.com:443", 0, true)
	if rep.OK {
		t.Fatal("missing client config should not be ok")
	}
	if rep.Kind != "client" || !rep.SecretsRedacted || rep.ChangesSystem || rep.ChangesNetwork || rep.RequiresRoot {
		t.Fatalf("unexpected client report metadata: %+v", rep)
	}
	if got := findCheck(rep.Checks, "config_readable"); got.Status != "fail" {
		t.Fatalf("config_readable = %+v, want fail", got)
	}
	if got := findCheck(rep.Checks, "udp_policy"); got.Status != "ok" || !strings.Contains(got.Detail, "relayed through bx tunnel") {
		t.Fatalf("udp_policy = %+v, want ok relay by default", got)
	}
	var buf bytes.Buffer
	if err := writeJSON(&buf, rep); err != nil {
		t.Fatal(err)
	}
	var parsed doctorReport
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("json should be parseable: %v\n%s", err, buf.String())
	}
	if parsed.Kind != "client" {
		t.Fatalf("parsed kind = %q", parsed.Kind)
	}
}

func TestClientDoctorIncludesPlatformChecks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.yaml")
	rep := collectClientDoctor(path, "example.com:443", 0, true)
	// terminal_proxy 是 collectTerminalProxyChecks 在所有 GOOS 上都会产出的检查名,
	// 用它证明 collectPlatformChecks 的结果已经并入 doctor(darwin 额外检查不便跨平台断言)。
	if got := findCheck(rep.Checks, "terminal_proxy"); got.Name == "" {
		t.Fatalf("doctor checks missing terminal_proxy platform check: %+v", rep.Checks)
	}
}

func TestCollectClientDoctorWithIncludePlatformChecksToggle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.yaml")

	withPlatform := collectClientDoctorWith(path, "example.com:443", 0, true, true)
	if got := findCheck(withPlatform.Checks, "terminal_proxy"); got.Name == "" {
		t.Fatalf("includePlatformChecks=true should include terminal_proxy check: %+v", withPlatform.Checks)
	}

	withoutPlatform := collectClientDoctorWith(path, "example.com:443", 0, true, false)
	if got := findCheck(withoutPlatform.Checks, "terminal_proxy"); got.Name != "" {
		t.Fatalf("includePlatformChecks=false must not include terminal_proxy check: %+v", withoutPlatform.Checks)
	}
}

func TestStatusReportIncludesTruthfulGuardianRecovery(t *testing.T) {
	started := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	rep := assembleClientStatusReport(
		stats.Report{TunnelHealthy: true, LatencyMS: 18},
		guardian.Status{
			SchemaVersion:     1,
			Protection:        guardian.ProtectionProtected,
			DNSState:          guardian.DNSManaged,
			DNSManaged:        true,
			NetworkGeneration: "wifi-b",
			Recovery: guardian.RecoverySnapshot{
				ID:         "recovery-8",
				State:      "running",
				Stage:      "transport_health",
				Reason:     "underlay_changed",
				Generation: "wifi-b",
				Attempt:    2,
				StartedAt:  started,
				UpdatedAt:  started.Add(time.Second),
			},
		},
	)

	var encoded map[string]any
	data, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &encoded); err != nil {
		t.Fatal(err)
	}
	if encoded["protection_state"] != guardian.ProtectionRecovering {
		t.Fatalf("protection_state = %v, want recovering", encoded["protection_state"])
	}
	if encoded["network_generation"] != "wifi-b" {
		t.Fatalf("network_generation = %v, want wifi-b", encoded["network_generation"])
	}
	if encoded["core_available"] != true || encoded["core_evidence"] != "local_status_socket" {
		t.Fatalf("Core evidence = available:%v evidence:%v, want local status socket", encoded["core_available"], encoded["core_evidence"])
	}
	recovery, ok := encoded["recovery"].(map[string]any)
	if !ok || recovery["recovery_id"] != "recovery-8" || recovery["stage"] != "transport_health" {
		t.Fatalf("recovery = %#v", encoded["recovery"])
	}
}

func TestStatusReportIncludesGuardianDNSState(t *testing.T) {
	rep := assembleClientStatusReport(stats.Report{TunnelHealthy: true}, guardian.Status{
		Protection: guardian.ProtectionProtected,
		DNSState:   guardian.DNSManaged,
		DNSManaged: true,
		DNSService: "Wi-Fi",
	})
	data, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"\"dns_state\":\"managed\"",
		"\"dns_managed\":true",
		"\"dns_service\":\"Wi-Fi\"",
	} {
		if !bytes.Contains(data, []byte(want)) {
			t.Fatalf("missing %s: %s", want, data)
		}
	}
	if got := renderClientStatus(rep); !strings.Contains(got, "DNS     Wi-Fi managed") {
		t.Fatalf("human status = %q, want managed Wi-Fi DNS", got)
	}
}

func TestDarwinStatusDowngradesProtectedWhenGuardianDNSIsNotManaged(t *testing.T) {
	var legacy guardian.Status
	if err := json.Unmarshal([]byte(`{"protection_state":"protected"}`), &legacy); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name   string
		status guardian.Status
	}{
		{name: "legacy Guardian JSON without dns_state", status: legacy},
		{name: "unknown DNS", status: guardian.Status{Protection: guardian.ProtectionProtected, DNSState: guardian.DNSUnknown}},
		{name: "unmanaged DNS", status: guardian.Status{Protection: guardian.ProtectionProtected, DNSState: guardian.DNSUnmanaged}},
		{name: "managed state without managed evidence", status: guardian.Status{Protection: guardian.ProtectionProtected, DNSState: guardian.DNSManaged}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rep, err := readClientStatusReportWith(
				func() (stats.Report, error) { return stats.Report{TunnelHealthy: true}, nil },
				func() (guardian.Status, error) { return tt.status, nil },
				"darwin",
			)
			if err != nil {
				t.Fatal(err)
			}
			if rep.ProtectionState != guardian.ProtectionNeedsAttention {
				t.Fatalf("status = %+v, want needs_attention", rep)
			}
		})
	}

	rep, err := readClientStatusReportWith(
		func() (stats.Report, error) { return stats.Report{TunnelHealthy: true}, nil },
		func() (guardian.Status, error) {
			return guardian.Status{Protection: guardian.ProtectionProtected, DNSState: guardian.DNSUnknown}, nil
		},
		"linux",
	)
	if err != nil {
		t.Fatal(err)
	}
	if rep.ProtectionState != guardian.ProtectionProtected {
		t.Fatalf("non-Darwin status = %+v, want protected", rep)
	}
}

func TestStatusUsesGuardianWhenCoreSocketIsUnavailable(t *testing.T) {
	coreCalls := 0
	guardianCalls := 0
	rep, err := readClientStatusReportWith(
		func() (stats.Report, error) {
			coreCalls++
			return stats.Report{}, errors.New("missing Core socket")
		},
		func() (guardian.Status, error) {
			guardianCalls++
			return guardian.Status{
				Protection: guardian.ProtectionNeedsAttention,
				Recovery: guardian.RecoverySnapshot{
					State: "failed", Stage: "verify", ErrorCode: "verification_failed",
				},
			}, nil
		},
		"darwin",
	)
	if err != nil {
		t.Fatalf("status rejected authoritative Guardian state: %v", err)
	}
	if coreCalls != 1 || guardianCalls != 1 {
		t.Fatalf("status calls Core=%d Guardian=%d, want one each", coreCalls, guardianCalls)
	}
	if rep.ProtectionState != guardian.ProtectionNeedsAttention {
		t.Fatalf("partial status = %+v, want Repair Required", rep)
	}
	if got := renderClientStatus(rep); !strings.Contains(got, "Repair Required") {
		t.Fatalf("human status = %q, want Repair Required", got)
	}
}

func TestCoreUnavailableStatusIsPartialAndTruthful(t *testing.T) {
	tests := []struct {
		name      string
		status    guardian.Status
		wantState string
		wantLabel string
	}{
		{
			name:      "Guardian off",
			status:    guardian.Status{Protection: guardian.ProtectionOff, Recovery: guardian.RecoverySnapshot{State: "idle", Stage: "idle"}},
			wantState: guardian.ProtectionOff,
			wantLabel: "Off",
		},
		{
			name: "Guardian blocked",
			status: guardian.Status{
				Protection: guardian.ProtectionBlocked,
				Recovery:   guardian.RecoverySnapshot{State: "failed", Stage: "transport_health", ErrorCode: "transport_unavailable"},
			},
			wantState: guardian.ProtectionBlocked,
			wantLabel: "Blocked",
		},
		{
			name: "Guardian needs attention outranks recovery",
			status: guardian.Status{
				Protection: guardian.ProtectionNeedsAttention,
				Recovery:   guardian.RecoverySnapshot{State: "running", Stage: "verify"},
			},
			wantState: guardian.ProtectionNeedsAttention,
			wantLabel: "Repair Required",
		},
		{
			name: "Guardian recovering",
			status: guardian.Status{
				Protection: guardian.ProtectionRecovering,
				Recovery:   guardian.RecoverySnapshot{State: "running", Stage: "transport_health"},
			},
			wantState: guardian.ProtectionRecovering,
			wantLabel: "Reconnecting",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rep, err := readClientStatusReportWith(
				func() (stats.Report, error) { return stats.Report{}, errors.New("missing Core socket") },
				func() (guardian.Status, error) { return tt.status, nil },
				"darwin",
			)
			if err != nil {
				t.Fatal(err)
			}
			if rep.ProtectionState != tt.wantState {
				t.Fatalf("protection_state = %q, want %q", rep.ProtectionState, tt.wantState)
			}

			data, err := json.Marshal(rep)
			if err != nil {
				t.Fatal(err)
			}
			var encoded map[string]any
			if err := json.Unmarshal(data, &encoded); err != nil {
				t.Fatal(err)
			}
			if encoded["core_available"] != false || encoded["core_evidence"] != "unavailable" {
				t.Fatalf("Core evidence = available:%v evidence:%v, want unavailable", encoded["core_available"], encoded["core_evidence"])
			}
			for _, key := range []string{"server", "socks_addr", "tunnel_healthy", "latency_ms", "restarts", "udp_mode"} {
				if _, ok := encoded[key]; ok {
					t.Fatalf("partial JSON fabricated unevidenced Core field %q: %s", key, data)
				}
			}

			human := renderClientStatus(rep)
			for _, want := range []string{tt.wantLabel, "Core", "Unavailable", "cannot be verified"} {
				if !strings.Contains(human, want) {
					t.Fatalf("partial human status = %q, want %q", human, want)
				}
			}
			for _, forbidden := range []string{"bx 状态", "kill-switch", "真实 IP", "隧道", "健康"} {
				if strings.Contains(human, forbidden) {
					t.Fatalf("partial human status made unevidenced Core claim %q: %s", forbidden, human)
				}
			}
		})
	}
}

func TestStatusDoesNotClaimProtectedWithoutCoreEvidence(t *testing.T) {
	rep, err := readClientStatusReportWith(
		func() (stats.Report, error) { return stats.Report{}, errors.New("missing Core socket") },
		func() (guardian.Status, error) {
			return guardian.Status{
				Protection: guardian.ProtectionProtected,
				Recovery:   guardian.RecoverySnapshot{State: "idle", Stage: "idle"},
			}, nil
		},
		"darwin",
	)
	if err != nil {
		t.Fatal(err)
	}
	if rep.ProtectionState != guardian.ProtectionNeedsAttention {
		t.Fatalf("Core-less status = %+v, want fail-closed needs_attention", rep)
	}
}

func TestCLIRepairRequiredOutranksTransientRecovery(t *testing.T) {
	rep := assembleClientStatusReport(stats.Report{TunnelHealthy: true}, guardian.Status{
		Protection: guardian.ProtectionNeedsAttention,
		Recovery:   guardian.RecoverySnapshot{State: "accepted", Stage: "queued"},
	})
	if rep.ProtectionState != guardian.ProtectionNeedsAttention {
		t.Fatalf("protection = %q, want needs_attention", rep.ProtectionState)
	}
	if got := renderClientStatus(rep); !strings.Contains(got, "Repair Required") || strings.Contains(got, "Status  Reconnecting") {
		t.Fatalf("human status = %q, want Repair Required precedence", got)
	}
}

func TestHumanStatusDistinguishesRecoverySafetyStates(t *testing.T) {
	tests := []struct {
		name       string
		protection string
		recovery   guardian.RecoverySnapshot
		want       string
	}{
		{
			name:       "reconnecting",
			protection: guardian.ProtectionRecovering,
			recovery:   guardian.RecoverySnapshot{State: "running", Stage: "verify"},
			want:       "Reconnecting",
		},
		{
			name:       "blocked",
			protection: guardian.ProtectionBlocked,
			recovery:   guardian.RecoverySnapshot{State: "failed", Stage: "transport_health", ErrorCode: "transport_unavailable"},
			want:       "Blocked",
		},
		{
			name:       "repair required",
			protection: guardian.ProtectionNeedsAttention,
			recovery:   guardian.RecoverySnapshot{State: "failed", Stage: "verify", ErrorCode: "verification_failed"},
			want:       "Repair Required",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := renderClientStatus(clientStatusReport{
				Report:          &stats.Report{TunnelHealthy: true},
				CoreAvailable:   true,
				CoreEvidence:    "local_status_socket",
				ProtectionState: tt.protection,
				Recovery:        tt.recovery,
			})
			if !strings.Contains(got, tt.want) {
				t.Fatalf("status output = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDarwinStatusFallbackRequiresRepairWhenGuardianIsUnavailable(t *testing.T) {
	status := guardianStatusFallback(stats.Report{TunnelHealthy: true}, "darwin")
	if status.Protection != guardian.ProtectionNeedsAttention {
		t.Fatalf("darwin fallback protection = %q, want needs_attention", status.Protection)
	}
	if status.Recovery.State != "failed" || status.Recovery.ErrorCode != "recovery_unavailable" {
		t.Fatalf("darwin fallback recovery = %+v", status.Recovery)
	}

	linux := guardianStatusFallback(stats.Report{TunnelHealthy: true}, "linux")
	if linux.Protection != guardian.ProtectionProtected {
		t.Fatalf("linux fallback protection = %q, want protected", linux.Protection)
	}
}

func TestDoctorReportsLatestRecoveryWithoutDirectFallback(t *testing.T) {
	check := recoveryDoctorCheck(guardian.RecoverySnapshot{
		ID:         "recovery-8",
		State:      "failed",
		Stage:      "transport_health",
		Reason:     "underlay_changed",
		Generation: "wifi-b",
		ErrorCode:  "transport_unavailable",
		Attempt:    3,
	})
	if check.Name != "network_recovery" || check.Status != "warn" {
		t.Fatalf("recovery doctor check = %+v", check)
	}
	if !strings.Contains(check.Detail, "stage=transport_health") ||
		!strings.Contains(check.Detail, "error_code=transport_unavailable") {
		t.Fatalf("recovery doctor detail = %q", check.Detail)
	}
	guidance := strings.ToLower(check.Hint)
	if strings.Contains(guidance, "direct") || strings.Contains(guidance, "fallback") {
		t.Fatalf("doctor suggested unsafe fallback: %q", check.Hint)
	}
	if !strings.Contains(guidance, "bx logs") || !strings.Contains(guidance, "bx reconnect") {
		t.Fatalf("doctor hint = %q, want logs and troubleshooting reconnect", check.Hint)
	}
}

func TestGuardianDNSDoctorCheck(t *testing.T) {
	managed := guardianDNSDoctorCheck(guardian.Status{
		DNSState: guardian.DNSManaged, DNSManaged: true, DNSService: "Wi-Fi",
	})
	if managed.Status != "ok" {
		t.Fatalf("managed = %+v", managed)
	}
	unmanaged := guardianDNSDoctorCheck(guardian.Status{DNSState: guardian.DNSUnmanaged})
	if unmanaged.Status != "fail" || unmanaged.Hint == "" {
		t.Fatalf("unmanaged = %+v", unmanaged)
	}
}

func TestClientDoctorReportsProxyUDPPolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("server: \"brook://x\"\nudp:\n  mode: proxy\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rep := collectClientDoctor(path, "example.com:443", 0, true)
	got := findCheck(rep.Checks, "udp_policy")
	if got.Status != "ok" || !strings.Contains(got.Detail, "relayed through bx tunnel") || got.Hint != "" {
		t.Fatalf("udp_policy = %+v, want ok proxy relay", got)
	}
}

func TestClientDoctorReportsBlockedUDPPolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("server: \"brook://x\"\nudp:\n  mode: block\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rep := collectClientDoctor(path, "example.com:443", 0, true)
	got := findCheck(rep.Checks, "udp_policy")
	if got.Status != "warn" || !strings.Contains(got.Hint, "Google Meet") || !strings.Contains(got.Hint, "sudo bx realtime on") {
		t.Fatalf("udp_policy = %+v, want block warning with realtime hint", got)
	}
}

func TestClientInspectIncludesDoctorAndStatusError(t *testing.T) {
	t.Setenv("BX_STATUS_SOCKET", filepath.Join(t.TempDir(), "missing.sock"))
	path := filepath.Join(t.TempDir(), "missing.yaml")
	rep := collectClientInspect(path, "example.com:443", 0, true)
	if rep.OK {
		t.Fatal("missing config and missing status socket should not be ok")
	}
	if !rep.SecretsRedacted || rep.ChangesSystem || rep.ChangesNetwork {
		t.Fatalf("unexpected inspect metadata: %+v", rep)
	}
	if rep.Capabilities.Product != "bx" {
		t.Fatalf("capabilities product = %q", rep.Capabilities.Product)
	}
	if rep.Doctor.Kind != "client" {
		t.Fatalf("doctor kind = %q", rep.Doctor.Kind)
	}
	if rep.StatusError == "" {
		t.Fatal("inspect should keep status socket failure as data")
	}
	if len(rep.NextActions) == 0 {
		t.Fatal("inspect should include next actions")
	}
	var buf bytes.Buffer
	if err := writeJSON(&buf, rep); err != nil {
		t.Fatal(err)
	}
	var parsed inspectReport
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("json should be parseable: %v\n%s", err, buf.String())
	}
}

func TestWebRTCCheckLowRiskWhenUDPRelayed(t *testing.T) {
	cfg := &config.Config{UDP: config.UDP{Mode: "proxy", Transport: "hysteria2://x"}}
	rep := assessWebRTCCheck(cfg, &stats.Report{
		TunnelHealthy: true,
		UDPMode:       "proxy",
		UDPTransport:  "hysteria2@example.com",
	}, nil, install.DNSStatus{Supported: true, Enabled: true, Service: "Wi-Fi"}, nil)
	if !rep.OK || rep.Risk != "low" {
		t.Fatalf("webrtc report = %+v, want ok low", rep)
	}
	if !rep.BrowserVerificationRequired || rep.LeakProof != "not_proven" {
		t.Fatalf("webrtc report should keep browser verification boundary: %+v", rep)
	}
	if got := findCheck(rep.Checks, "udp_path"); got.Status != "ok" || !strings.Contains(got.Detail, "relayed") {
		t.Fatalf("udp_path = %+v, want relayed ok", got)
	}
}

func TestWebRTCCheckHighRiskForDirectUDP(t *testing.T) {
	cfg := &config.Config{UDP: config.UDP{Mode: "direct-realtime"}}
	rep := assessWebRTCCheck(cfg, &stats.Report{
		TunnelHealthy: true,
		UDPMode:       "direct-realtime",
	}, nil, install.DNSStatus{Supported: true, Enabled: true, Service: "Wi-Fi"}, nil)
	if rep.OK || rep.Risk != "high" {
		t.Fatalf("webrtc report = %+v, want high risk", rep)
	}
	if got := findCheck(rep.Checks, "udp_path"); got.Status != "fail" || !strings.Contains(got.Detail, "real network") {
		t.Fatalf("udp_path = %+v, want direct UDP fail", got)
	}
}

func TestApplyBrowserICECandidatesDetectsUnexpectedPublicIP(t *testing.T) {
	rep := webrtcCheckReport{Risk: "low", LeakProof: "not_proven", BrowserVerificationRequired: true}
	applyBrowserICECandidates(&rep, browserICEResult{
		Candidates: []string{
			"candidate:1 1 udp 2122260223 203.0.113.10 55000 typ srflx raddr 192.168.1.5 rport 55000",
		},
	}, []string{"203.0.113.20"})
	if rep.OK || rep.Risk != "high" || rep.LeakProof != "unexpected_public_ip_detected" {
		t.Fatalf("browser candidates should detect unexpected public IP: %+v", rep)
	}
	if got := findCheck(rep.Checks, "browser_unexpected_public_ip"); got.Status != "fail" || !strings.Contains(got.Detail, "203.0.113.10") || strings.Contains(got.Hint, "real public IP") {
		t.Fatalf("browser_unexpected_public_ip = %+v, want unexpected public IP without real-IP overclaim", got)
	}
}

func TestApplyBrowserICECandidatesAllowsExpectedProxyIP(t *testing.T) {
	rep := webrtcCheckReport{Risk: "low", LeakProof: "not_proven", BrowserVerificationRequired: true}
	applyBrowserICECandidates(&rep, browserICEResult{
		Candidates: []string{
			"candidate:1 1 udp 2122260223 203.0.113.20 55000 typ srflx",
		},
	}, []string{"203.0.113.20"})
	if !rep.OK || rep.Risk != "low" || rep.LeakProof != "no_public_leak_detected" {
		t.Fatalf("expected proxy IP should be accepted: %+v", rep)
	}
	if got := findCheck(rep.Checks, "browser_expected_public_ip"); got.Status != "ok" || !strings.Contains(got.Detail, "203.0.113.20") {
		t.Fatalf("browser_expected_public_ip = %+v, want expected proxy IP", got)
	}
}

func TestApplyBrowserICECandidatesWithoutExpectedIPCannotProveSafety(t *testing.T) {
	rep := webrtcCheckReport{Risk: "low", LeakProof: "not_proven", BrowserVerificationRequired: true}
	applyBrowserICECandidates(&rep, browserICEResult{
		Candidates: []string{
			"candidate:1 1 udp 2122260223 203.0.113.10 55000 typ srflx",
		},
	}, nil)
	if rep.OK || rep.Risk != "high" || rep.LeakProof != "public_ip_detected_without_expected" {
		t.Fatalf("public IP without expectation should be inconclusive/high: %+v", rep)
	}
}

func TestApplyBrowserICECandidatesFlagsLANAddress(t *testing.T) {
	rep := webrtcCheckReport{Risk: "low", LeakProof: "not_proven", BrowserVerificationRequired: true}
	applyBrowserICECandidates(&rep, browserICEResult{
		Candidates: []string{
			"candidate:1 1 udp 2122260223 192.168.50.18 55000 typ host",
		},
	}, nil)
	if rep.OK || rep.Risk != "medium" || rep.LeakProof != "local_network_candidate_detected" {
		t.Fatalf("LAN candidate should be medium risk: %+v", rep)
	}
}

func TestApplyBrowserICECandidatesIgnoresUnspecifiedAddress(t *testing.T) {
	rep := webrtcCheckReport{Risk: "low", LeakProof: "not_proven", BrowserVerificationRequired: true}
	applyBrowserICECandidates(&rep, browserICEResult{
		Candidates: []string{
			"candidate:1 1 udp 2122260223 0.0.0.0 9 typ host",
		},
	}, nil)
	if !rep.OK || rep.Risk != "low" || rep.LeakProof != "no_public_leak_detected" {
		t.Fatalf("0.0.0.0 should not count as leak: %+v", rep)
	}
}

func TestAssembleLeakCheckReportAggregatesNetworkRisks(t *testing.T) {
	doctor := doctorReport{Kind: "client", Version: "test", SecretsRedacted: true}
	doctor.addCheck("service_active", "ok", "active", "")
	webrtc := webrtcCheckReport{
		OK:              true,
		Kind:            "webrtc",
		Version:         "test",
		SecretsRedacted: true,
		Risk:            "low",
		LeakProof:       "no_public_leak_detected",
		Checks: []checkReport{
			{Name: "dns", Status: "ok", Detail: "system DNS -> 127.0.0.1"},
			{Name: "udp_path", Status: "ok", Detail: "non-DNS UDP relayed through bx tunnel"},
			{Name: "browser_public_ip", Status: "ok", Detail: "no unexpected public IP"},
		},
	}
	rep := assembleLeakCheckReport(doctor, webrtc, nil)
	if !rep.OK || rep.Risk != "low" {
		t.Fatalf("leak report = %+v, want ok low", rep)
	}
	for _, name := range []string{"service", "dns", "udp", "webrtc", "ipv6", "quic"} {
		if got := findCheck(rep.Checks, name); got.Name == "" {
			t.Fatalf("leak report missing %s: %+v", name, rep.Checks)
		}
	}
}

func TestAssembleLeakCheckReportRaisesRiskForWebRTC(t *testing.T) {
	doctor := doctorReport{Kind: "client", Version: "test", SecretsRedacted: true}
	webrtc := webrtcCheckReport{
		Risk:      "high",
		LeakProof: "unexpected_public_ip_detected",
		Checks: []checkReport{
			{Name: "browser_unexpected_public_ip", Status: "fail", Detail: "203.0.113.10"},
		},
	}
	rep := assembleLeakCheckReport(doctor, webrtc, nil)
	if rep.OK || rep.Risk != "high" {
		t.Fatalf("leak report = %+v, want high", rep)
	}
	if got := findCheck(rep.Checks, "webrtc"); got.Status != "fail" || !strings.Contains(got.Detail, "unexpected_public_ip_detected") {
		t.Fatalf("webrtc aggregate = %+v, want fail proof", got)
	}
}

func TestAssessNetworkProbeAcceptsExpectedIPv4Exit(t *testing.T) {
	report := assessNetworkProbe(networkProbeResult{
		IPv4:   "203.0.113.20",
		DNSIPs: []string{"198.18.0.42"},
	}, []string{"203.0.113.20"})
	if !report.OK || report.Risk != "low" {
		t.Fatalf("network probe report = %+v, want ok low", report)
	}
	if got := findCheck(report.Checks, "egress_ipv4"); got.Status != "ok" || !strings.Contains(got.Detail, "203.0.113.20") {
		t.Fatalf("egress_ipv4 = %+v, want expected ok", got)
	}
	if got := findCheck(report.Checks, "dns_resolution"); got.Status != "ok" || !strings.Contains(got.Detail, "fake-IP") {
		t.Fatalf("dns_resolution = %+v, want fake-IP ok", got)
	}
}

func TestAssessNetworkProbeFlagsUnexpectedIPv4Exit(t *testing.T) {
	report := assessNetworkProbe(networkProbeResult{IPv4: "203.0.113.10"}, []string{"203.0.113.20"})
	if report.OK || report.Risk != "high" {
		t.Fatalf("network probe report = %+v, want high risk", report)
	}
	if got := findCheck(report.Checks, "egress_ipv4"); got.Status != "fail" || !strings.Contains(got.Detail, "203.0.113.10") {
		t.Fatalf("egress_ipv4 = %+v, want unexpected fail", got)
	}
}

func TestAssessNetworkProbeFlagsPublicIPv6Egress(t *testing.T) {
	report := assessNetworkProbe(networkProbeResult{IPv6: "2001:db8::1"}, nil)
	if report.OK || report.Risk != "high" {
		t.Fatalf("network probe report = %+v, want high risk", report)
	}
	if got := findCheck(report.Checks, "egress_ipv6"); got.Status != "fail" || !strings.Contains(got.Detail, "2001:db8::1") {
		t.Fatalf("egress_ipv6 = %+v, want public IPv6 fail", got)
	}
}

func TestAssessNetworkProbeDoesNotTreatIPv4BodyAsIPv6Leak(t *testing.T) {
	report := assessNetworkProbe(networkProbeResult{IPv4: "203.0.113.10", IPv6: "203.0.113.10"}, []string{"203.0.113.10"})
	if !report.OK || report.Risk != "low" {
		t.Fatalf("network probe report = %+v, want low risk", report)
	}
	if got := findCheck(report.Checks, "egress_ipv6"); got.Status != "info" || !strings.Contains(got.Detail, "IPv4 address") {
		t.Fatalf("egress_ipv6 = %+v, want IPv4 body info", got)
	}
}

func TestAssembleLeakCheckReportIncludesNetworkProbe(t *testing.T) {
	doctor := doctorReport{Kind: "client", Version: "test", SecretsRedacted: true}
	doctor.addCheck("service_active", "ok", "active", "")
	webrtc := webrtcCheckReport{OK: true, Risk: "low"}
	network := &networkProbeReport{
		OK:   true,
		Risk: "low",
		Checks: []checkReport{
			{Name: "egress_ipv4", Status: "ok", Detail: "203.0.113.20"},
			{Name: "egress_ipv6", Status: "ok", Detail: "no IPv6 egress observed"},
		},
	}
	rep := assembleLeakCheckReport(doctor, webrtc, network)
	if !rep.OK || rep.Network == nil {
		t.Fatalf("leak report = %+v, want ok with network probe", rep)
	}
	if got := findCheck(rep.Checks, "egress_ipv4"); got.Status != "ok" {
		t.Fatalf("egress_ipv4 aggregate = %+v, want ok", got)
	}
}

func TestAssessObserveWindowWarnsOnUDPBlocks(t *testing.T) {
	start := stats.Report{Snapshot: stats.Snapshot{Proxy: 10, Direct: 2, Blocked: 0, UDPBlocked: 1, BytesDown: 1000}}
	end := stats.Report{Snapshot: stats.Snapshot{Proxy: 15, Direct: 3, Blocked: 0, UDPBlocked: 4, BytesDown: 9000}, TunnelHealthy: true, UDPMode: "proxy"}
	rep := assessObserveWindow([]stats.Report{start, end}, 30*time.Second)
	if rep.OK || rep.Risk != "medium" {
		t.Fatalf("observe report = %+v, want medium risk", rep)
	}
	if got := findCheck(rep.Checks, "udp_blocks"); got.Status != "warn" || !strings.Contains(got.Detail, "3") {
		t.Fatalf("udp_blocks = %+v, want warn with delta", got)
	}
	if len(rep.Recommendations) == 0 {
		t.Fatalf("observe report should include recommendations: %+v", rep)
	}
}

func TestAssessObserveWindowSuggestsReproducingWhenNoActivity(t *testing.T) {
	start := stats.Report{Snapshot: stats.Snapshot{Proxy: 10, Direct: 2}}
	end := stats.Report{Snapshot: stats.Snapshot{Proxy: 10, Direct: 2}, TunnelHealthy: true}
	rep := assessObserveWindow([]stats.Report{start, end}, 30*time.Second)
	if rep.OK || rep.Risk != "medium" {
		t.Fatalf("observe report = %+v, want medium risk", rep)
	}
	if got := findCheck(rep.Checks, "activity"); got.Status != "warn" || !strings.Contains(got.Hint, "reproduce") {
		t.Fatalf("activity = %+v, want reproduce hint", got)
	}
}

func TestAssessObserveWindowVideoScenarioAddsTestSteps(t *testing.T) {
	start := stats.Report{Snapshot: stats.Snapshot{Proxy: 10, Direct: 0}}
	end := stats.Report{Snapshot: stats.Snapshot{Proxy: 20, Direct: 0, BytesDown: 20000}, TunnelHealthy: true, UDPMode: "proxy"}
	rep := assessObserveWindow([]stats.Report{start, end}, 30*time.Second, "video")
	if len(rep.TestSteps) == 0 {
		t.Fatalf("video observe should include test steps: %+v", rep)
	}
	if got := findCheck(rep.Checks, "split_shape"); got.Status != "info" || !strings.Contains(got.Detail, "proxy") {
		t.Fatalf("split_shape = %+v, want proxy info", got)
	}
	if !containsText(rep.Recommendations, "CDN") {
		t.Fatalf("video recommendations should mention CDN: %+v", rep.Recommendations)
	}
}

func TestArchiveClientLogsRecordsReason(t *testing.T) {
	dir, err := archiveClientLogsWithReason(t.TempDir(), "doctor")
	if err != nil {
		t.Fatal(err)
	}
	meta, err := os.ReadFile(filepath.Join(dir, "meta.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(meta), "reason=doctor") {
		t.Fatalf("meta should include archive reason:\n%s", meta)
	}
	for _, name := range []string{"doctor.json", "recovery.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("archive should include %s: %v", name, err)
		}
	}
}

func TestPersistRecoverySnapshotRedactsFreeFormTransportError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recovery.json")
	secret := "vless://user:password@example.test?token=secret"
	if err := persistRecoverySnapshot(path, guardian.RecoverySnapshot{
		ID:        "recovery-8",
		State:     "failed",
		Stage:     "transport_health",
		ErrorCode: "future_secret_error",
		Detail:    secret,
	}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), secret) || strings.Contains(string(data), "future_secret_error") {
		t.Fatalf("recovery archive leaked free-form failure: %s", data)
	}
	var got guardian.RecoverySnapshot
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.ErrorCode != "recovery_failed" || got.Detail != "" {
		t.Fatalf("recovery archive = %+v, want stable redacted failure", got)
	}
}

func TestAutoArchiveAfterClientCommandRecordsExactError(t *testing.T) {
	root := t.TempDir()
	t.Setenv("BX_LOG_ARCHIVE_DIR", root)
	t.Setenv("BX_STATUS_SOCKET", filepath.Join(root, "missing.sock"))

	commandErr := fmt.Errorf("control request failed: %w", context.DeadlineExceeded)
	autoArchiveAfterClientCommand("reconnect", &commandErr, false)

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("archive entries = %d, want 1", len(entries))
	}
	got, err := os.ReadFile(filepath.Join(root, entries[0].Name(), "command-error.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "context deadline exceeded\n" {
		t.Fatalf("command error = %q, want exact context deadline error", got)
	}
	if strings.Contains(string(got), "transport failure") {
		t.Fatalf("command error was rewritten as transport failure: %q", got)
	}
}

func TestAutoArchiveAfterClientCommandRedactsErrorDetails(t *testing.T) {
	root := t.TempDir()
	t.Setenv("BX_LOG_ARCHIVE_DIR", root)
	t.Setenv("BX_STATUS_SOCKET", filepath.Join(root, "missing.sock"))

	commandErr := errors.New("control-plane secret=token-value server detail")
	autoArchiveAfterClientCommand("reconnect", &commandErr, false)

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(root, entries[0].Name(), "command-error.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "client command failed\n" {
		t.Fatalf("command error = %q, want redacted diagnostic", got)
	}
	if strings.Contains(string(got), "token-value") || strings.Contains(string(got), "server detail") {
		t.Fatalf("secret-bearing command error persisted: %q", got)
	}
}

func TestLogsReportFromTailSuccess(t *testing.T) {
	rep := logsReportFromTail("client", 25, "line1\nline2\n", nil)
	if !rep.OK || rep.Kind != "logs" || rep.Service != "client" || rep.Lines != 25 || rep.Text != "line1\nline2\n" || rep.Error != "" {
		t.Fatalf("logs report = %+v, want successful report", rep)
	}
}

func TestLogsReportFromTailErrorKeepsTextAndHint(t *testing.T) {
	rep := logsReportFromTail("client", 0, "partial\n", os.ErrPermission)
	if rep.OK || rep.Lines != 100 || rep.Text != "partial\n" || rep.Error == "" || !strings.Contains(rep.Hint, "sudo bx logs") {
		t.Fatalf("logs report = %+v, want error report with partial text and hint", rep)
	}
}

func TestDefaultLogArchiveRootIsAbsolute(t *testing.T) {
	t.Setenv("BX_LOG_ARCHIVE_DIR", "")
	root := defaultLogArchiveRoot()
	if root == "" || !filepath.IsAbs(root) {
		t.Fatalf("default log archive root should be absolute, got %q", root)
	}
}

func TestDefaultLogArchiveRootHonorsEnv(t *testing.T) {
	want := filepath.Join(t.TempDir(), "diagnostics")
	t.Setenv("BX_LOG_ARCHIVE_DIR", want)
	if got := defaultLogArchiveRoot(); got != want {
		t.Fatalf("default log archive root = %q, want env %q", got, want)
	}
}

func TestApplyPlatformRiskRaisesRiskForTailscaleWarn(t *testing.T) {
	rep := leakCheckReport{
		Risk: "low",
		Checks: []checkReport{
			{Name: "tailscale", Status: "warn", Detail: "overlay route absent"},
		},
	}
	applyPlatformRisk(&rep)
	if rep.OK || rep.Risk != "medium" {
		t.Fatalf("tailscale warning should make leak report medium risk: %+v", rep)
	}
}

func TestPruneLogArchivesKeepsNewest(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 15; i++ {
		name := filepath.Join(root, "bx-logs-20260101-1200"+leftPadInt(i, 2))
		if err := os.MkdirAll(name, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := pruneLogArchives(root, 12); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, entry := range entries {
		if entry.IsDir() {
			got = append(got, entry.Name())
		}
	}
	if len(got) != 12 {
		t.Fatalf("dirs after prune = %d, want 12: %v", len(got), got)
	}
	if _, err := os.Stat(filepath.Join(root, "bx-logs-20260101-120000")); !os.IsNotExist(err) {
		t.Fatalf("oldest archive should be pruned, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "bx-logs-20260101-120014")); err != nil {
		t.Fatalf("newest archive should be kept: %v", err)
	}
}

func TestPruneLogArchivesAllowsFewerThanKeep(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "bx-logs-20260101-120000"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := pruneLogArchives(root, 12); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "bx-logs-20260101-120000")); err != nil {
		t.Fatalf("single archive should be kept: %v", err)
	}
}

func TestCapabilitiesReport(t *testing.T) {
	rep := capabilities()
	if rep.SchemaVersion != 1 || rep.Product != "bx" || !rep.SecretsRedacted {
		t.Fatalf("unexpected capabilities metadata: %+v", rep)
	}
	doctor := findCapability(rep.Commands, "bx doctor --json")
	if !doctor.Stable || doctor.RequiresRoot || doctor.ChangesSystem || doctor.ChangesNetwork || !doctor.ReadsSecrets {
		t.Fatalf("unexpected doctor capability: %+v", doctor)
	}
	if len(doctor.Arguments) == 0 || len(doctor.Examples) == 0 {
		t.Fatalf("doctor capability should include arguments/examples: %+v", doctor)
	}
	inspect := findCapability(rep.Commands, "bx inspect --json")
	if !inspect.Stable || inspect.RequiresRoot || inspect.ChangesSystem || inspect.ChangesNetwork || !inspect.ReadsSecrets {
		t.Fatalf("unexpected inspect capability: %+v", inspect)
	}
	webrtc := findCapability(rep.Commands, "bx webrtc-check --json")
	if !webrtc.Stable || webrtc.RequiresRoot || webrtc.ChangesSystem || webrtc.ChangesNetwork || !webrtc.ReadsSecrets {
		t.Fatalf("unexpected webrtc-check capability: %+v", webrtc)
	}
	notes := strings.Join(webrtc.SafeNotes, " ")
	if !strings.Contains(notes, "ICE candidates") || !strings.Contains(notes, "prove a WebRTC public-IP leak") {
		t.Fatalf("webrtc-check should describe browser ICE proof capability: %+v", webrtc)
	}
	leak := findCapability(rep.Commands, "bx leak-check --json")
	if !leak.Stable || leak.RequiresRoot || leak.ChangesSystem || leak.ChangesNetwork || !leak.ReadsSecrets {
		t.Fatalf("unexpected leak-check capability: %+v", leak)
	}
	if !strings.Contains(strings.Join(leak.SafeNotes, " "), "network-path") {
		t.Fatalf("leak-check should state network-path scope: %+v", leak)
	}
	observe := findCapability(rep.Commands, "bx observe --json")
	if !observe.Stable || observe.RequiresRoot || observe.ChangesSystem || observe.ChangesNetwork {
		t.Fatalf("unexpected observe capability: %+v", observe)
	}
	if !strings.Contains(strings.Join(observe.SafeNotes, " "), "status socket") {
		t.Fatalf("observe should document local-only status socket sampling: %+v", observe)
	}
	setup := findCapability(rep.Commands, "sudo bx setup <client-link>")
	if setup.Command == "" || !strings.Contains(strings.Join(setup.Arguments, " "), "<client-link>") {
		t.Fatalf("setup capability should use client-link wording: %+v", setup)
	}
	invite := findCapability(rep.Commands, "sudo bx invite [name]")
	if invite.Command == "" || !invite.RequiresRoot || !invite.ChangesSystem || invite.ChangesNetwork {
		t.Fatalf("unexpected invite capability: %+v", invite)
	}
	if !strings.Contains(strings.Join(invite.SafeNotes, " "), "preferred human-facing") {
		t.Fatalf("invite capability should guide agents toward human sharing: %+v", invite)
	}
	serverInstall := findCapability(rep.Commands, "sudo bx server install --host <host>")
	if serverInstall.Command == "" || !serverInstall.RequiresRoot || !serverInstall.ChangesSystem || serverInstall.ChangesNetwork {
		t.Fatalf("unexpected server install capability: %+v", serverInstall)
	}
	if !strings.Contains(strings.Join(serverInstall.Arguments, " "), "--open-ufw") {
		t.Fatalf("server install capability should advertise --open-ufw: %+v", serverInstall)
	}
	if !strings.Contains(strings.Join(serverInstall.SafeNotes, " "), "May change firewall only when --open-ufw is passed.") {
		t.Fatalf("server install capability should document --open-ufw firewall note: %+v", serverInstall)
	}
	userList := findCapability(rep.Commands, "sudo bx user list --json")
	if userList.Command == "" || !userList.RequiresRoot || userList.ChangesSystem || userList.ChangesNetwork {
		t.Fatalf("unexpected user list capability: %+v", userList)
	}
	userInvite := findCapability(rep.Commands, "sudo bx user invite <name>")
	if userInvite.Command == "" || !userInvite.RequiresRoot || !userInvite.ChangesSystem || userInvite.ChangesNetwork {
		t.Fatalf("unexpected user invite capability: %+v", userInvite)
	}
	probe := findCapability(rep.Commands, "bx probe <client-link>")
	if probe.Command == "" || !strings.Contains(strings.Join(probe.Examples, " "), "<client-link>") {
		t.Fatalf("probe capability should use client-link wording: %+v", probe)
	}
	up := findCapability(rep.Commands, "sudo bx up")
	if !up.RequiresRoot || !up.ChangesSystem || !up.ChangesNetwork {
		t.Fatalf("unexpected up capability: %+v", up)
	}
	status := findCapability(rep.Commands, "bx status --json")
	if !status.Stable || status.RequiresRoot || status.ChangesSystem || status.ChangesNetwork {
		t.Fatalf("unexpected status json capability: %+v", status)
	}
	if !strings.Contains(strings.Join(status.SafeNotes, " "), "menu bar") {
		t.Fatalf("status json should mention status surfaces: %+v", status)
	}
	reconnect := findCapability(rep.Commands, "sudo bx reconnect")
	if !reconnect.Stable || !reconnect.RequiresRoot || reconnect.ChangesSystem || reconnect.ChangesNetwork {
		t.Fatalf("unexpected reconnect capability: %+v", reconnect)
	}
	if !strings.Contains(strings.Join(reconnect.SafeNotes, " "), "Does not release") {
		t.Fatalf("reconnect should document its fail-closed scope: %+v", reconnect)
	}
	if restart := findCapability(rep.Commands, "sudo bx restart"); restart.Command != "" {
		t.Fatalf("legacy restart must not be advertised to agents: %+v", restart)
	}
	update := findCapability(rep.Commands, "sudo bx update")
	if !update.Stable || !update.RequiresRoot || !update.ChangesSystem || update.ChangesNetwork {
		t.Fatalf("unexpected update capability: %+v", update)
	}
	if !strings.Contains(strings.Join(update.SafeNotes, " "), "Does not restart") {
		t.Fatalf("update should document preserved protection: %+v", update)
	}
	updateNotes := strings.Join(update.SafeNotes, " ")
	if !strings.Contains(updateNotes, "fail-closed") || !strings.Contains(updateNotes, "rolls back") {
		t.Fatalf("update should document fail-closed Guardian transaction semantics: %+v", update)
	}
	if !strings.Contains(updateNotes, "do not simulate an update by combining down and up") {
		t.Fatalf("update should tell agents not to simulate updates with down/up: %+v", update)
	}
	direct := findCapability(rep.Commands, "sudo bx direct add <domain>")
	if !direct.Stable || !direct.RequiresRoot || !direct.ChangesSystem || direct.ChangesNetwork || !direct.ReadsSecrets {
		t.Fatalf("unexpected direct add capability: %+v", direct)
	}
	if !strings.Contains(strings.Join(direct.SafeNotes, " "), "mutually exclusive") {
		t.Fatalf("direct add should document direct/proxy mutual exclusion: %+v", direct)
	}
	proxy := findCapability(rep.Commands, "sudo bx proxy add <domain>")
	if !proxy.Stable || !proxy.RequiresRoot || !proxy.ChangesSystem || proxy.ChangesNetwork || !proxy.ReadsSecrets {
		t.Fatalf("unexpected proxy add capability: %+v", proxy)
	}
	if !strings.Contains(strings.Join(proxy.SafeNotes, " "), "force tunnel") {
		t.Fatalf("proxy add should document force tunnel behavior: %+v", proxy)
	}
	presetApply := findCapability(rep.Commands, "sudo bx preset apply <name>")
	if !presetApply.Stable || !presetApply.RequiresRoot || !presetApply.ChangesSystem || presetApply.ChangesNetwork || !presetApply.ReadsSecrets {
		t.Fatalf("unexpected preset apply capability: %+v", presetApply)
	}
	presetNotes := strings.ToLower(strings.Join(presetApply.SafeNotes, " "))
	if !strings.Contains(presetNotes, "explicit opt-in") || !strings.Contains(presetNotes, "direct") || !strings.Contains(presetNotes, "dns") {
		t.Fatalf("preset apply should document opt-in config-only behavior: %+v", presetApply)
	}
	logs := findCapability(rep.Commands, "bx logs")
	if !logs.Stable || logs.ChangesSystem || logs.ChangesNetwork {
		t.Fatalf("unexpected logs capability: %+v", logs)
	}
	if !strings.Contains(strings.ToLower(strings.Join(logs.SafeNotes, " ")), "automatic diagnostics") {
		t.Fatalf("logs capability should mention automatic diagnostics archive: %+v", logs)
	}
	udpStatus := findCapability(rep.Commands, "bx realtime status")
	if !udpStatus.Stable || udpStatus.RequiresRoot || udpStatus.ChangesSystem || udpStatus.ChangesNetwork {
		t.Fatalf("unexpected realtime status capability: %+v", udpStatus)
	}
	if !strings.Contains(strings.Join(udpStatus.SafeNotes, " "), "UDP") {
		t.Fatalf("realtime status should mention UDP: %+v", udpStatus)
	}
	realtimeOn := findCapability(rep.Commands, "sudo bx realtime on")
	if !realtimeOn.Stable || !realtimeOn.RequiresRoot || !realtimeOn.ChangesSystem || realtimeOn.ChangesNetwork || !realtimeOn.ReadsSecrets {
		t.Fatalf("unexpected realtime on capability: %+v", realtimeOn)
	}
	if !strings.Contains(strings.Join(realtimeOn.SafeNotes, " "), "Relays non-DNS UDP") {
		t.Fatalf("realtime on should document UDP relay behavior: %+v", realtimeOn)
	}
	if !strings.Contains(strings.Join(realtimeOn.SafeNotes, " "), "without restarting") {
		t.Fatalf("realtime on should document preserved protection: %+v", realtimeOn)
	}
	realtimeOff := findCapability(rep.Commands, "sudo bx realtime off")
	if !realtimeOff.Stable || !realtimeOff.RequiresRoot || !realtimeOff.ChangesSystem || realtimeOff.ChangesNetwork || !realtimeOff.ReadsSecrets {
		t.Fatalf("unexpected realtime off capability: %+v", realtimeOff)
	}
	dnsOn := findCapability(rep.Commands, "sudo bx dns on")
	if !dnsOn.RequiresRoot || !dnsOn.ChangesSystem || !dnsOn.ChangesNetwork {
		t.Fatalf("unexpected dns on capability: %+v", dnsOn)
	}
	menuInstall := findCapability(rep.Commands, "scripts/install-macos-menu.sh install")
	if menuInstall.Command == "" || menuInstall.RequiresRoot || menuInstall.ChangesNetwork || menuInstall.ChangesSystem {
		t.Fatalf("unexpected macOS menu install capability: %+v", menuInstall)
	}
	if strings.Contains(strings.ToLower(menuInstall.Summary), "companion") {
		t.Fatalf("macOS menu install should describe the app as the default menu bar app, not a companion: %+v", menuInstall)
	}
	if !strings.Contains(strings.Join(menuInstall.SafeNotes, " "), "Does not start protection") {
		t.Fatalf("macOS menu install should clarify it does not start protection: %+v", menuInstall)
	}
	menuStatus := findCapability(rep.Commands, "scripts/install-macos-menu.sh status")
	if menuStatus.Command == "" || !menuStatus.Stable || menuStatus.ChangesSystem || menuStatus.ChangesNetwork {
		t.Fatalf("unexpected macOS menu status capability: %+v", menuStatus)
	}
	menuRestart := findCapability(rep.Commands, "scripts/install-macos-menu.sh restart")
	if menuRestart.Command == "" || menuRestart.RequiresRoot || menuRestart.ChangesSystem || menuRestart.ChangesNetwork {
		t.Fatalf("unexpected macOS menu restart capability: %+v", menuRestart)
	}
	if strings.Contains(strings.ToLower(strings.Join([]string{menuRestart.Summary, strings.Join(menuRestart.SafeNotes, " ")}, " ")), "companion") {
		t.Fatalf("macOS menu restart should describe the menu bar app, not a companion: %+v", menuRestart)
	}
	if !strings.Contains(strings.Join(menuRestart.SafeNotes, " "), "not protection") {
		t.Fatalf("macOS menu restart should clarify it does not restart protection: %+v", menuRestart)
	}
	menuUninstall := findCapability(rep.Commands, "scripts/install-macos-menu.sh uninstall")
	if menuUninstall.Command == "" || menuUninstall.RequiresRoot || menuUninstall.ChangesSystem || menuUninstall.ChangesNetwork {
		t.Fatalf("unexpected macOS menu uninstall capability: %+v", menuUninstall)
	}
	if !strings.Contains(strings.Join(menuUninstall.SafeNotes, " "), "Does not turn off protection") {
		t.Fatalf("macOS menu uninstall should clarify it does not turn off protection: %+v", menuUninstall)
	}
	macRelease := findCapability(rep.Commands, "scripts/package-macos-release.sh")
	if macRelease.Command == "" || !macRelease.Stable || macRelease.RequiresRoot || macRelease.ChangesSystem || macRelease.ChangesNetwork {
		t.Fatalf("unexpected macOS release capability: %+v", macRelease)
	}
	macReleaseVerify := findCapability(rep.Commands, "scripts/verify-macos-release.sh")
	if macReleaseVerify.Command == "" || !macReleaseVerify.Stable || macReleaseVerify.RequiresRoot || macReleaseVerify.ChangesSystem || macReleaseVerify.ChangesNetwork {
		t.Fatalf("unexpected macOS release verify capability: %+v", macReleaseVerify)
	}
	var buf bytes.Buffer
	if err := writeJSON(&buf, rep); err != nil {
		t.Fatal(err)
	}
	var parsed capabilitiesReport
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("capabilities json should be parseable: %v\n%s", err, buf.String())
	}
}

func leftPadInt(v, width int) string {
	s := strconv.Itoa(v)
	for len(s) < width {
		s = "0" + s
	}
	return s
}

func TestRenderRealtimeStatusFallback(t *testing.T) {
	out := renderRealtimeStatus(nil)
	for _, want := range []string{
		"realtime supported: true",
		"udp mode: proxy",
		"udp blocked: unknown",
		"relayed through bx tunnel",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("realtime fallback missing %q:\n%s", want, out)
		}
	}
}

func TestRenderRealtimeStatusFromReport(t *testing.T) {
	out := renderRealtimeStatus(&stats.Report{
		Snapshot: stats.Snapshot{UDPBlocked: 42},
		UDPMode:  "block",
		UDPNote:  "custom udp note",
	})
	for _, want := range []string{
		"udp mode: block",
		"udp blocked: 42",
		"custom udp note",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("realtime report missing %q:\n%s", want, out)
		}
	}
}

func TestSetRealtimeModeUpdatesClientConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("server: \"brook://x\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := setRealtimeMode(path, "proxy"); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.UDP.Mode != "proxy" {
		t.Fatalf("udp mode after on = %q, want proxy", cfg.UDP.Mode)
	}
	if err := setRealtimeMode(path, "block"); err != nil {
		t.Fatal(err)
	}
	cfg, err = loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.UDP.Mode != "block" {
		t.Fatalf("udp mode after off = %q, want block", cfg.UDP.Mode)
	}
}

func TestPlanRealtimePostChange(t *testing.T) {
	tests := []struct {
		name          string
		unitInstalled bool
		activeState   string
		wantContains  string
	}{
		{"active stays protected", true, "active", "保持运行"},
		{"not installed", false, "inactive", "sudo bx up"},
		{"inactive installed", true, "inactive", "下次 sudo bx up"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := planRealtimePostChange(tt.unitInstalled, tt.activeState)
			if !strings.Contains(got.Message, tt.wantContains) {
				t.Fatalf("plan = %+v, want message containing %q", got, tt.wantContains)
			}
		})
	}
}

func TestSetRealtimeModePreservesBXLink(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	link := blink.Encode("brook://server?server=example.com%3A443&password=pw")
	if err := os.WriteFile(path, []byte("server: \""+link+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := setRealtimeMode(path, "direct-realtime"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	if !strings.Contains(text, link) {
		t.Fatalf("setRealtimeMode should preserve bx link, got:\n%s", text)
	}
	if strings.Contains(text, "brook://server?") {
		t.Fatalf("setRealtimeMode should not rewrite bx link to internal link:\n%s", text)
	}
}

func TestRealtimeReportFromConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("server: \"brook://x\"\nudp:\n  mode: proxy\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rep := realtimeReportFromConfig(path)
	if rep == nil {
		t.Fatal("expected realtime report from config")
	}
	if rep.UDPMode != "proxy" || !strings.Contains(rep.UDPNote, "relayed through bx tunnel") {
		t.Fatalf("report = %+v, want proxy relay note", rep)
	}
}

func TestServerDoctorJSONReport(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "server.yaml")
	sharesDir := filepath.Join(dir, "shares")
	if err := writeServerConfig(cfgPath, serverConfig{Listen: ":10998", Password: "secret"}, false); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sharesDir, 0o700); err != nil {
		t.Fatal(err)
	}
	rep := collectServerDoctor(cfgPath, sharesDir)
	if rep.Kind != "server" || !rep.SecretsRedacted || !rep.RequiresRoot {
		t.Fatalf("unexpected server report metadata: %+v", rep)
	}
	if got := findCheck(rep.Checks, "config_parse"); got.Status != "ok" {
		t.Fatalf("config_parse = %+v, want ok", got)
	}
	if got := findCheck(rep.Checks, "shares"); got.Status != "info" || got.Detail != "none" {
		t.Fatalf("shares = %+v, want none info", got)
	}
}

func TestShareJSONViewsExposeOnlyOperationalFields(t *testing.T) {
	shares := []shareInfo{{
		Name:   "alice",
		Config: serverConfig{Listen: ":10001", Password: "pw"},
	}}
	views := shareViews(shares)
	if len(views) != 1 || views[0].Name != "alice" || views[0].Listen != ":10001" {
		t.Fatalf("share views = %+v", views)
	}
	var buf bytes.Buffer
	if err := writeJSON(&buf, sharesReport{OK: true, SecretsRedacted: true, Shares: views}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "pw") {
		t.Fatalf("shares json should not expose password: %s", buf.String())
	}
}

func findCheck(checks []checkReport, name string) checkReport {
	for _, check := range checks {
		if check.Name == name {
			return check
		}
	}
	return checkReport{}
}

func containsText(values []string, pattern string) bool {
	for _, value := range values {
		if strings.Contains(value, pattern) {
			return true
		}
	}
	return false
}

func findCapability(commands []commandCapability, command string) commandCapability {
	for _, item := range commands {
		if item.Command == command {
			return item
		}
	}
	return commandCapability{}
}

func appHasCommand(app *cli.App, name string) bool {
	return findAppCommand(app, name) != nil
}

func findAppCommand(app *cli.App, name string) *cli.Command {
	for _, command := range app.Commands {
		if command.Name == name {
			return command
		}
	}
	return nil
}

func commandHasSubcommand(command *cli.Command, name string) bool {
	for _, sub := range command.Subcommands {
		if sub.Name == name {
			return true
		}
	}
	return false
}

func subcommandHidden(command *cli.Command, name string) bool {
	for _, sub := range command.Subcommands {
		if sub.Name == name {
			return sub.Hidden
		}
	}
	return false
}

func commandHasFlag(command *cli.Command, name string) bool {
	for _, flag := range command.Flags {
		for _, flagName := range flag.Names() {
			if flagName == name {
				return true
			}
		}
	}
	return false
}

func TestIsListening(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if !isListening(port) {
		t.Fatalf("port %s should be detected as listening", port)
	}
}

func TestCopyIfExists(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "source.log")
	dst := filepath.Join(dir, "copy.log")
	if err := os.WriteFile(src, []byte("raw log\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := copyIfExists(src, dst); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "raw log\n" {
		t.Fatalf("copy = %q", b)
	}
	if err := copyIfExists(filepath.Join(dir, "missing.log"), filepath.Join(dir, "missing-copy.log")); err != nil {
		t.Fatalf("missing source should be ignored: %v", err)
	}
}

func TestWriteReadServerConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.yaml")
	cfg := serverConfig{Listen: ":9999", Password: "pw"}
	if err := writeServerConfig(path, cfg, false); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("server config perm = %o, want 0600", fi.Mode().Perm())
	}
	got, err := readServerConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != cfg {
		t.Fatalf("config = %+v, want %+v", got, cfg)
	}
}

func TestWriteServerConfigForceResetsPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.yaml")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeServerConfig(path, serverConfig{Listen: ":9999", Password: "pw"}, true); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("server config perm = %o, want 0600", fi.Mode().Perm())
	}
}

func TestRotateServerConfigPreservesListenAndResetsPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.yaml")
	if err := writeServerConfig(path, serverConfig{Listen: ":9999", Password: "old"}, false); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := rotateServerConfig(path, "new")
	if err != nil {
		t.Fatal(err)
	}
	if got.Listen != ":9999" || got.Password != "new" {
		t.Fatalf("rotated config = %+v", got)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("server config perm = %o, want 0600", fi.Mode().Perm())
	}
}

func TestShareHelpers(t *testing.T) {
	if got, err := cleanShareName("alice-1"); err != nil || got != "alice-1" {
		t.Fatalf("cleanShareName = %q, %v", got, err)
	}
	for _, bad := range []string{"", "../x", "a b", "x/y"} {
		if _, err := cleanShareName(bad); err == nil {
			t.Fatalf("bad share name %q should fail", bad)
		}
	}
	dir := t.TempDir()
	if got := shareConfigPath(dir, "alice"); got != filepath.Join(dir, "alice.yaml") {
		t.Fatalf("shareConfigPath = %q", got)
	}
}

func TestStringFlagReadsPostArgFlags(t *testing.T) {
	args := []string{"alice", "--host", "example.com", "--listen=:10077"}
	if got := stringFlagFromArgs(args, "host"); got != "example.com" {
		t.Fatalf("host = %q", got)
	}
	if got := stringFlagFromArgs(args, "listen"); got != ":10077" {
		t.Fatalf("listen = %q", got)
	}
}

func TestReadSharesSorted(t *testing.T) {
	dir := t.TempDir()
	for _, item := range []struct {
		name   string
		listen string
	}{
		{"bob", ":10002"},
		{"alice", ":10001"},
	} {
		if err := writeServerConfig(shareConfigPath(dir, item.name), serverConfig{Listen: item.listen, Password: "pw"}, false); err != nil {
			t.Fatal(err)
		}
	}
	got, err := readShares(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "alice" || got[1].Name != "bob" {
		t.Fatalf("shares = %+v", got)
	}
}

func TestUserViewsFromShares(t *testing.T) {
	users := userViews([]shareInfo{
		{Name: "alice", Config: serverConfig{Type: "reality", Link: "vless://id@example.com:443"}},
		{Name: "bob", Config: serverConfig{Listen: ":10000", Password: "pw"}},
	})
	if len(users) != 2 {
		t.Fatalf("users = %+v", users)
	}
	if users[0].Name != "alice" || users[0].Type != "reality" || users[0].Status != "active" || users[0].Plan != "default" {
		t.Fatalf("reality user view = %+v", users[0])
	}
	if users[1].Name != "bob" || users[1].Type != "brook" || users[1].Listen != ":10000" || users[1].Plan != "default" {
		t.Fatalf("brook user view = %+v", users[1])
	}
}

func TestReadShare(t *testing.T) {
	dir := t.TempDir()
	if err := writeServerConfig(shareConfigPath(dir, "alice"), serverConfig{Type: "reality", Link: "vless://id@example.com:443"}, false); err != nil {
		t.Fatal(err)
	}
	got, err := readShare(dir, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "alice" || got.Config.Type != "reality" {
		t.Fatalf("readShare = %+v", got)
	}
}

func TestNextShareListenSkipsExistingShares(t *testing.T) {
	dir := t.TempDir()
	if err := writeServerConfig(shareConfigPath(dir, "alice"), serverConfig{Listen: ":10000", Password: "pw"}, false); err != nil {
		t.Fatal(err)
	}
	got, err := nextShareListen(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != ":10001" {
		t.Fatalf("nextShareListen = %q, want :10001", got)
	}
}

func TestSetupCommandQuotesLinks(t *testing.T) {
	main := "bx://main"
	udp := "bx://udp"
	if got := setupCommand(main, ""); got != "sudo bx setup 'bx://main'" {
		t.Fatalf("setupCommand main = %q", got)
	}
	if got := setupCommand(main, udp); got != "sudo bx setup 'bx://main' --udp 'bx://udp'" {
		t.Fatalf("setupCommand udp = %q", got)
	}
}

func TestInviteTextIsUserFriendly(t *testing.T) {
	out := inviteText("alice", "bx://main", "bx://udp")
	for _, want := range []string{"bx invite: alice", "给用户", "菜单栏 App", "bx://main", "UDP: bx://udp", "sudo bx setup 'bx://main' --udp 'bx://udp'", "sudo bx up"} {
		if !strings.Contains(out, want) {
			t.Fatalf("invite text missing %q:\n%s", want, out)
		}
	}
}

func TestInviteShareConfigShowsExistingBrookShare(t *testing.T) {
	dir := t.TempDir()
	if err := writeServerConfig(shareConfigPath(dir, "alice"), serverConfig{Listen: ":10000", Password: "pw"}, false); err != nil {
		t.Fatal(err)
	}
	cfg, err := inviteShareConfig("alice", filepath.Join(t.TempDir(), "server.yaml"), dir, "example.com")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Link == "" || !strings.Contains(cfg.Link, "brook://") || !strings.Contains(cfg.Link, "example.com") {
		t.Fatalf("invite share cfg link = %q", cfg.Link)
	}
}

func TestResolveConfigPathKeepsExplicitMissingPath(t *testing.T) {
	// 用户显式传入的不存在路径应原样返回(不偷偷回退),便于错误信息指向用户路径
	p := "/nonexistent/explicit/whoami-bx-test.yaml"
	if got := resolveConfigPath(p); got != p {
		t.Fatalf("显式缺失路径应原样返回, got %q", got)
	}
}

func TestMCPInstallText(t *testing.T) {
	out := mcpInstallText("/usr/local/bin/bx")
	for _, want := range []string{
		"claude mcp add --scope user bx -- /usr/local/bin/bx mcp",
		`"command": "/usr/local/bin/bx"`,
		`"args": ["mcp"]`,
		"AI agent",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("mcpInstallText 缺 %q\n--- got ---\n%s", want, out)
		}
	}
}

func TestRawLinkRisk(t *testing.T) {
	// 裸凭据链接 → 提示
	for _, raw := range []string{
		"vless://uuid@1.2.3.4:9998?security=reality&pbk=K&sid=ab&sni=www.apple.com",
		"brook://server?server=1.2.3.4%3A9999&password=pw",
		"  vless://x@h:1 ", // 带空白也认
	} {
		if rawLinkRisk(raw) == "" {
			t.Errorf("裸链接应提示风险: %q", raw)
		}
	}
	// 已换壳 / 非链接 → 不提示
	for _, wrapped := range []string{
		"bx://eyJ2IjoxfQ",
		"blink://abc",
		"",
		"garbage",
	} {
		if rawLinkRisk(wrapped) != "" {
			t.Errorf("已换壳/非裸链接不该提示: %q", wrapped)
		}
	}
}

func TestProtocolAdvisory(t *testing.T) {
	// 弱协议(对当今强 DPI/探测易识别)→ 建议换 REALITY
	for _, weak := range []string{
		"trojan://pw@1.2.3.4:443?sni=x.com",
		"ss://YWVzLTI1Ni1nY206cHc@1.2.3.4:8388",
		"vmess://eyJhZGQiOiIxLjIuMy40IiwicG9ydCI6IjQ0MyIsImlkIjoieCIsIm5ldCI6InRjcCJ9",
	} {
		a := protocolAdvisory(weak)
		if a == "" || !strings.Contains(a, "REALITY") {
			t.Errorf("弱协议应提示换 REALITY: %q → %q", weak, a)
		}
	}
	// hysteria2 缺 obfs → 提示加 salamander
	if a := protocolAdvisory("hysteria2://pw@1.2.3.4:8443?sni=x.com"); !strings.Contains(a, "obfs") {
		t.Errorf("裸 hysteria2 应提示加 obfs: %q", a)
	}
	// hysteria2 已带 obfs → 不提示
	if a := protocolAdvisory("hysteria2://pw@1.2.3.4:8443?sni=x.com&obfs=salamander&obfs-password=p"); a != "" {
		t.Errorf("带 obfs 的 hysteria2 不该提示: %q", a)
	}
	// reality / brook(一等公民/默认)→ 不提示
	for _, ok := range []string{
		"vless://uuid@1.2.3.4:9998?security=reality&pbk=K&sid=ab&sni=www.apple.com",
		"brook://server?server=1.2.3.4%3A9999&password=pw",
	} {
		if a := protocolAdvisory(ok); a != "" {
			t.Errorf("reality/brook 不该提示: %q → %q", ok, a)
		}
	}
}

func TestResolveConfigLinksBundle(t *testing.T) {
	l1 := "vless://u@1.2.3.4:9998?security=reality&pbk=K&sid=ab&sni=www.apple.com"
	l2 := "brook://server?server=1.2.3.4%3A9999&password=pw"
	bundle := blink.EncodeMulti([]string{l1, l2})
	probe, configLinks, err := resolveConfigLinks(bundle)
	if err != nil {
		t.Fatalf("resolve bundle: %v", err)
	}
	if probe != l1 {
		t.Fatalf("probe 应=主传输 %q, got %q", l1, probe)
	}
	if len(configLinks) != 2 {
		t.Fatalf("应 2 个 configLink, got %d", len(configLinks))
	}
	// 各自换壳,解回应等于原 link
	for i, want := range []string{l1, l2} {
		got, err := blink.Decode(configLinks[i])
		if err != nil || got != want {
			t.Fatalf("configLink[%d] 解回=%q want=%q err=%v", i, got, want, err)
		}
	}
}

func TestResolveConfigLinksRawSingle(t *testing.T) {
	raw := "vless://u@h:1?security=reality&pbk=K&sid=a&sni=s"
	probe, configLinks, err := resolveConfigLinks(raw)
	if err != nil || probe != raw || len(configLinks) != 1 {
		t.Fatalf("裸单链接: probe=%q n=%d err=%v", probe, len(configLinks), err)
	}
}

func TestClientDoctorVlessServerLinkOK(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	os.WriteFile(path, []byte("server: \"vless://u@1.2.3.4:9998?security=reality&pbk=K&sid=ab&sni=www.apple.com\"\n"), 0o600)
	rep := collectClientDoctor(path, "x:443", 0, true)
	got := findCheck(rep.Checks, "server_link")
	if got.Status != "ok" {
		t.Fatalf("vless server_link 应 ok,实得 %+v", got)
	}
}

func TestVlessUUIDHelpers(t *testing.T) {
	link := "vless://old-uuid-1234@1.2.3.4:443?security=reality&pbk=P&sid=ab&sni=www.cloudflare.com"
	if got := uuidFromVlessLink(link); got != "old-uuid-1234" {
		t.Errorf("extract uuid: got %q", got)
	}
	swapped := swapVlessUUID(link, "new-uuid-5678")
	if uuidFromVlessLink(swapped) != "new-uuid-5678" {
		t.Errorf("swap uuid 失败: %q", swapped)
	}
	// 其余部分(host/port/query)不变
	if !strings.Contains(swapped, "@1.2.3.4:443?security=reality&pbk=P&sid=ab&sni=www.cloudflare.com") {
		t.Errorf("swap 不该动其余部分: %q", swapped)
	}
	// 非 vless 链接
	if uuidFromVlessLink("brook://x") != "" {
		t.Error("非 vless 应返回空")
	}
}

func TestStatusReportIncludesUpdateObservabilityFields(t *testing.T) {
	rep := assembleClientStatusReport(
		stats.Report{TunnelHealthy: true},
		guardian.Status{
			Protection:      guardian.ProtectionProtected,
			Phase:           guardian.PhaseActivating,
			CoreVersion:     "1.2.3",
			GuardianVersion: "1.2.2",
			RuntimeVersion:  "1.2.3",
		},
	)

	var encoded map[string]any
	data, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &encoded); err != nil {
		t.Fatal(err)
	}

	// Check all four fields are present and correct
	if encoded["phase"] != "activating" {
		t.Fatalf("phase = %v, want activating", encoded["phase"])
	}
	if encoded["core_version"] != "1.2.3" {
		t.Fatalf("core_version = %v, want 1.2.3", encoded["core_version"])
	}
	if encoded["guardian_version"] != "1.2.2" {
		t.Fatalf("guardian_version = %v, want 1.2.2", encoded["guardian_version"])
	}
	if encoded["runtime_version"] != "1.2.3" {
		t.Fatalf("runtime_version = %v, want 1.2.3", encoded["runtime_version"])
	}
}

func TestStatusReportOmitsEmptyPhase(t *testing.T) {
	rep := assembleClientStatusReport(
		stats.Report{TunnelHealthy: true},
		guardian.Status{
			Protection: guardian.ProtectionProtected,
			// Phase is empty (default)
		},
	)

	data, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}

	// Check that phase is not in the JSON (omitempty)
	if strings.Contains(string(data), "\"phase\"") {
		t.Fatalf("phase should be omitted when empty, but found in: %s", string(data))
	}
}

// 事故里「翻诊断包只拿到陈旧 Core 日志」正是因为归档只收 ClientLogPaths()。
// Guardian 失败的完整原因写在 Guardian 日志里,必须一并收进诊断包。
func TestArchiveClientLogsCollectsGuardianLogs(t *testing.T) {
	source := t.TempDir()
	guardLog := filepath.Join(source, "bx-guard.err.log")
	if err := os.WriteFile(guardLog, []byte("guardian_needs_attention code=core_ownership_uncertain\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	restore := swapGuardianArchiveLogPaths([]string{guardLog, filepath.Join(source, "absent.log")})
	defer restore()

	dir, err := archiveClientLogsWithReason(t.TempDir(), "up")
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "bx-guard.err.log"))
	if err != nil {
		t.Fatalf("诊断包必须包含 Guardian 日志:%v", err)
	}
	if !strings.Contains(string(got), "core_ownership_uncertain") {
		t.Errorf("Guardian 日志内容未收全,实际 = %q", got)
	}
}

// Guardian 日志是 0600 root-only:非 root 跑 bx logs --archive(以及 up/down
// 失败后的自动归档)读不到很正常,不能因此让整个诊断包失败。
func TestArchiveClientLogsSurvivesUnreadableGuardianLog(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 能读任何文件,无法构造不可读场景")
	}
	source := t.TempDir()
	guardLog := filepath.Join(source, "bx-guard.err.log")
	if err := os.WriteFile(guardLog, []byte("secret\n"), 0o000); err != nil {
		t.Fatal(err)
	}
	restore := swapGuardianArchiveLogPaths([]string{guardLog})
	defer restore()

	dir, err := archiveClientLogsWithReason(t.TempDir(), "up")
	if err != nil {
		t.Fatalf("Guardian 日志读不到不应让归档失败:%v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "meta.txt")); err != nil {
		t.Fatalf("归档仍应产出其余材料:%v", err)
	}
}

func swapGuardianArchiveLogPaths(paths []string) func() {
	previous := guardianArchiveLogPaths
	guardianArchiveLogPaths = func() []string { return paths }
	return func() { guardianArchiveLogPaths = previous }
}

// bx status --json 必须把观测与信念并列发布,并在二者不一致时明写 divergence。
// 真实事故形态:Guardian 报 protected,而流量其实走 en0 明文直连——旧的平坦
// 结构里这件事根本表达不出来。
func TestClientStatusPublishesObservationAndDivergence(t *testing.T) {
	protectedStatus := func() (guardian.Status, error) {
		return guardian.Status{
			Desired:    guardian.DesiredOn,
			Protection: guardian.ProtectionProtected,
			DNSState:   guardian.DNSManaged,
			DNSManaged: true,
		}, nil
	}
	healthyCore := func() (stats.Report, error) { return stats.Report{TunnelHealthy: true}, nil }

	t.Run("信念与事实不符时必须产出 divergence", func(t *testing.T) {
		rep, err := readClientStatusReportWithObserver(
			healthyCore, protectedStatus, "darwin",
			func(context.Context) observe.ObservedState {
				return observe.ObservedState{
					CaptureOK: observe.False, CaptureInterface: "en0",
					DNSManaged: observe.True, TunnelHealthy: observe.True,
				}
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		if rep.Observed == nil {
			t.Fatal("必须发布观测,否则消费者无从分辨哪些字段是记忆")
		}
		if rep.ProtectionState != guardian.ProtectionProtected {
			t.Errorf("观测不得覆盖信念,protection_state = %q, want protected", rep.ProtectionState)
		}
		var found bool
		for _, d := range rep.Divergence {
			if d.Field == "capture_ok" {
				found = true
			}
		}
		if !found {
			t.Errorf("劫持未生效却声称 protected 必须产出 capture_ok divergence,实际 = %+v", rep.Divergence)
		}

		encoded := map[string]any{}
		payload, err := json.Marshal(rep)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(payload, &encoded); err != nil {
			t.Fatal(err)
		}
		if encoded["observed"] == nil {
			t.Errorf("JSON 必须含 observed,实际 = %s", payload)
		}
		if encoded["divergence"] == nil {
			t.Errorf("JSON 必须含 divergence,实际 = %s", payload)
		}
		if encoded["desired"] != "on" {
			t.Errorf("JSON 必须发布意图 desired,实际 = %s", payload)
		}
	})

	t.Run("一致时必须安静", func(t *testing.T) {
		rep, err := readClientStatusReportWithObserver(
			healthyCore, protectedStatus, "darwin",
			func(context.Context) observe.ObservedState {
				return observe.ObservedState{
					CaptureOK: observe.True, CaptureInterface: "utun4",
					DNSManaged: observe.True, TunnelHealthy: observe.True,
					BarrierPresent: observe.False,
				}
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		if len(rep.Divergence) != 0 {
			t.Errorf("观测与信念一致时不应有 divergence,实际 = %+v", rep.Divergence)
		}
	})
}

// 观测不得改变 bx status 的成败:Core socket 不可达时该报错的仍报错,
// 能出报告的仍出报告。
func TestClientStatusObservationDoesNotChangeOutcome(t *testing.T) {
	observer := func(context.Context) observe.ObservedState {
		return observe.ObservedState{Errors: []observe.ObserveError{{Item: "capture_ok", Err: "boom"}}}
	}
	if _, err := readClientStatusReportWithObserver(
		func() (stats.Report, error) { return stats.Report{}, errors.New("missing Core socket") },
		func() (guardian.Status, error) { return guardian.Status{}, errors.New("guardian down") },
		"darwin", observer,
	); err == nil {
		t.Error("Core 与 Guardian 双双不可达时仍须报错,观测不得掩盖")
	}

	rep, err := readClientStatusReportWithObserver(
		func() (stats.Report, error) { return stats.Report{TunnelHealthy: true}, nil },
		func() (guardian.Status, error) {
			return guardian.Status{Desired: guardian.DesiredOn, Protection: guardian.ProtectionProtected}, nil
		},
		"linux", observer,
	)
	if err != nil {
		t.Fatalf("观测项失败不得让 bx status 失败:%v", err)
	}
	var explained bool
	for _, d := range rep.Divergence {
		if d.Field == "capture_ok" && d.Observed == "unknown" {
			explained = true
		}
	}
	if !explained {
		t.Errorf("观测失败必须以 unknown 的 divergence 现身,实际 = %+v", rep.Divergence)
	}
}

// desired=off 却观测到屏障/DNS 残留,是"用户已关闭保护但整机仍断网"这类事故的
// 唯一可见信号。
func TestClientStatusFlagsResidueWhenDesiredOff(t *testing.T) {
	rep, err := readClientStatusReportWithObserver(
		func() (stats.Report, error) { return stats.Report{}, nil },
		func() (guardian.Status, error) {
			return guardian.Status{Desired: guardian.DesiredOff, Protection: guardian.ProtectionOff}, nil
		},
		"darwin",
		func(context.Context) observe.ObservedState {
			return observe.ObservedState{BarrierPresent: observe.True, DNSManaged: observe.True}
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	fields := map[string]bool{}
	for _, d := range rep.Divergence {
		fields[d.Field] = true
	}
	if !fields["barrier_present"] || !fields["dns_managed"] {
		t.Errorf("关闭意图下的屏障/DNS 残留必须各产出一条 divergence,实际 = %+v", rep.Divergence)
	}
}
