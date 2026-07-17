package install

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestUnitText(t *testing.T) {
	u := UnitText("/usr/local/bin/bx run -c /etc/bx/config.yaml")
	for _, want := range []string{
		"[Unit]",
		"[Service]",
		"[Install]",
		"ExecStart=/usr/local/bin/bx run -c /etc/bx/config.yaml",
		"WantedBy=multi-user.target",
		"Restart=on-failure",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("unit 应含 %q,实际:\n%s", want, u)
		}
	}
}

func TestServerUnitTextIsHardened(t *testing.T) {
	u := ServerUnitText("/usr/local/bin/bx serve -c /etc/bx/server.yaml")
	for _, want := range []string{
		"ExecStart=/usr/local/bin/bx serve -c /etc/bx/server.yaml",
		"UMask=0077",
		"NoNewPrivileges=true",
		"PrivateTmp=true",
		"ProtectSystem=strict",
		"ProtectHome=true",
		"ReadOnlyPaths=/etc/bx",
		"ReadWritePaths=/var/lib/bx",
		// reality/hys2(内嵌 sing-box)需 AF_NETLINK(订阅路由)+ CAP_NET_BIND_SERVICE(绑 443),
		// 真机实测缺这俩 server 启动即 FATAL/bind 拒绝。锁住,别再退回会崩。
		"RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX AF_NETLINK",
		"CapabilityBoundingSet=CAP_NET_BIND_SERVICE",
		"AmbientCapabilities=CAP_NET_BIND_SERVICE",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("server unit 应含 %q,实际:\n%s", want, u)
		}
	}
}

func TestExecStartCmd(t *testing.T) {
	cases := []struct {
		name string
		unit string
		want string
	}{
		{"新版 run", UnitText("/usr/local/bin/bx run -c /etc/bx/config.yaml"), "run"},
		{"旧版 up(递归陷阱)", UnitText("/usr/local/bin/bx up -c /etc/bx/config.yaml"), "up"},
		{"无 ExecStart", "[Service]\nType=simple\n", ""},
		{"ExecStart 无子命令", "ExecStart=/usr/local/bin/bx\n", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := execStartCmd(c.unit); got != c.want {
				t.Fatalf("execStartCmd = %q, want %q", got, c.want)
			}
		})
	}
}

func TestLaunchdPlistText(t *testing.T) {
	plist := LaunchdPlistText("/usr/local/bin/bx run -c /etc/bx/config.yaml")
	for _, want := range []string{
		"<key>Label</key>",
		"<string>com.getbx.bx</string>",
		"<key>ProgramArguments</key>",
		"<string>/usr/local/bin/bx</string>",
		"<string>run</string>",
		"<string>-c</string>",
		"<string>/etc/bx/config.yaml</string>",
		"<key>RunAtLoad</key>",
		"<key>KeepAlive</key>",
		"<string>/var/log/bx.log</string>",
		"<string>/var/log/bx.err.log</string>",
	} {
		if !strings.Contains(plist, want) {
			t.Errorf("launchd plist 应含 %q,实际:\n%s", want, plist)
		}
	}
}

func TestLaunchdExecStartCmd(t *testing.T) {
	if got := launchdExecStartCmd(LaunchdPlistText("/usr/local/bin/bx run -c /etc/bx/config.yaml")); got != "run" {
		t.Fatalf("launchdExecStartCmd = %q, want run", got)
	}
	if got := launchdExecStartCmd(LaunchdPlistText("/usr/local/bin/bx up -c /etc/bx/config.yaml")); got != "up" {
		t.Fatalf("launchdExecStartCmd = %q, want up", got)
	}
}

func TestLaunchdEnableCommandsEnableBeforeBootstrap(t *testing.T) {
	cmds := launchdEnableCommands()
	want := []string{
		"bootout system /Library/LaunchDaemons/com.getbx.bx.plist",
		"enable system/com.getbx.bx",
		"bootstrap system /Library/LaunchDaemons/com.getbx.bx.plist",
		"kickstart -k system/com.getbx.bx",
	}
	if len(cmds) != len(want) {
		t.Fatalf("launchdEnableCommands len = %d, want %d", len(cmds), len(want))
	}
	for i := range want {
		if got := strings.Join(cmds[i], " "); got != want[i] {
			t.Fatalf("cmd[%d] = %q, want %q", i, got, want[i])
		}
	}
}

func TestLaunchdDisableCommandsStopLoadedLegacyService(t *testing.T) {
	cmds := launchdDisableCommands(map[string]bool{legacyLaunchdLabel: true})
	want := []string{
		"disable system/com.getbx.bx",
		"disable system/com.ggshr9.bx",
		"bootout system/com.ggshr9.bx",
	}
	if len(cmds) != len(want) {
		t.Fatalf("launchdDisableCommands len = %d, want %d", len(cmds), len(want))
	}
	for i := range want {
		if got := strings.Join(cmds[i], " "); got != want[i] {
			t.Fatalf("cmd[%d] = %q, want %q", i, got, want[i])
		}
	}
}

func TestLaunchdClientLabelsIncludeCurrentAndLegacy(t *testing.T) {
	got := launchdClientLabels()
	want := []string{launchdLabel, legacyLaunchdLabel}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("launchdClientLabels = %#v, want %#v", got, want)
	}
}

func TestAnyLaunchdClientServiceLoadedRecognizesLegacy(t *testing.T) {
	if !anyLaunchdClientServiceLoaded(map[string]bool{legacyLaunchdLabel: true}) {
		t.Fatal("legacy launchd service should count as active")
	}
	if anyLaunchdClientServiceLoaded(nil) {
		t.Fatal("empty launchd state should be inactive")
	}
}

func TestLaunchdBootoutErrorIgnoredAfterServiceStops(t *testing.T) {
	err := fmt.Errorf("launchctl bootout: exit status 3")
	if got := launchdBootoutResult(err, false); got != nil {
		t.Fatalf("stopped service should make bootout idempotent: %v", got)
	}
	if got := launchdBootoutResult(err, true); got == nil {
		t.Fatal("loaded service must preserve bootout failure")
	}
}

func TestLaunchdDisableCommandsAreIdempotentWhenNothingLoaded(t *testing.T) {
	cmds := launchdDisableCommands(nil)
	want := []string{
		"disable system/com.getbx.bx",
		"disable system/com.ggshr9.bx",
	}
	if len(cmds) != len(want) {
		t.Fatalf("launchdDisableCommands len = %d, want %d", len(cmds), len(want))
	}
	for i := range want {
		if got := strings.Join(cmds[i], " "); got != want[i] {
			t.Fatalf("cmd[%d] = %q, want %q", i, got, want[i])
		}
	}
}

func TestMigrateLegacyLaunchdPlistText(t *testing.T) {
	legacy := strings.Replace(
		LaunchdPlistText("/usr/local/bin/bx run -c /etc/bx/config.yaml --listen-dns 127.0.0.1:53"),
		launchdLabel,
		legacyLaunchdLabel,
		1,
	)
	got, err := migrateLegacyLaunchdPlistText(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "<string>"+legacyLaunchdLabel+"</string>") {
		t.Fatal("migrated plist should not retain legacy label")
	}
	for _, want := range []string{
		"<string>" + launchdLabel + "</string>",
		"<string>--listen-dns</string>",
		"<string>127.0.0.1:53</string>",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("migrated plist missing %q", want)
		}
	}
}

func TestExistingPaths(t *testing.T) {
	dir := t.TempDir()
	one := filepath.Join(dir, "one.log")
	two := filepath.Join(dir, "two.log")
	if err := os.WriteFile(one, []byte("ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := existingPaths(one, two)
	if len(got) != 1 || got[0] != one {
		t.Fatalf("existingPaths = %#v, want [%q]", got, one)
	}
}

func TestIsNetworkServiceLine(t *testing.T) {
	if !isNetworkServiceLine("(2) Wi-Fi") {
		t.Fatal("numbered service line should match")
	}
	if isNetworkServiceLine("(Hardware Port: Wi-Fi, Device: en0)") {
		t.Fatal("hardware detail line should not match")
	}
}

func TestShouldRefreshDNSStateWhenServiceChanges(t *testing.T) {
	if !shouldRefreshDNSState(dnsState{Service: "Wi-Fi", Servers: []string{"1.1.1.1"}}, "USB 10/100/1000 LAN") {
		t.Fatal("DNS state should refresh when current service differs from saved service")
	}
	if shouldRefreshDNSState(dnsState{Service: "Wi-Fi", Servers: []string{"1.1.1.1"}}, "Wi-Fi") {
		t.Fatal("DNS state should not refresh when current service matches saved service")
	}
}

func TestDNSRestoreArgs(t *testing.T) {
	got := dnsRestoreArgs(dnsState{Service: "Wi-Fi", Servers: []string{"1.1.1.1", "8.8.8.8"}})
	want := []string{"setdnsservers", "Wi-Fi", "1.1.1.1", "8.8.8.8"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("dnsRestoreArgs = %#v, want %#v", got, want)
	}
	got = dnsRestoreArgs(dnsState{Service: "Wi-Fi", Empty: true})
	want = []string{"setdnsservers", "Wi-Fi", "Empty"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("dnsRestoreArgs empty = %#v, want %#v", got, want)
	}
}

func TestDNSStateMissingRecoveryError(t *testing.T) {
	if err := dnsStateMissingRecoveryError(DNSStatus{Enabled: false}); err != nil {
		t.Fatalf("already-restored DNS should allow shutdown: %v", err)
	}
	if err := dnsStateMissingRecoveryError(DNSStatus{Enabled: true}); err == nil {
		t.Fatal("bx-managed DNS without saved state must refuse shutdown")
	}
}

func TestDNSStateMissingRecognizesWrappedNotExist(t *testing.T) {
	err := fmt.Errorf("读 DNS 状态: %w", os.ErrNotExist)
	if !dnsStateMissing(err) {
		t.Fatal("wrapped missing DNS state should be recognized")
	}
	if dnsStateMissing(fmt.Errorf("读 DNS 状态: %w", os.ErrPermission)) {
		t.Fatal("permission error must not be treated as missing DNS state")
	}
}

func TestRunNetworksetupContextWithRunnerHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	runner := &blockingDNSCommandRunner{entered: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		done <- runNetworksetupContextWithRunner(ctx, runner, "setdnsservers", "Wi-Fi", "Empty")
	}()
	select {
	case <-runner.entered:
	case <-time.After(time.Second):
		t.Fatal("networksetup runner was not entered")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("runNetworksetup error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("networksetup did not stop after context cancellation")
	}
}

func TestDisableDNSDarwinContextRetainsStateWhenCacheFlushCanceled(t *testing.T) {
	statePath := writeTestDNSState(t, dnsState{Service: "Wi-Fi", Servers: []string{"1.1.1.1"}})
	ctx, cancel := context.WithCancel(context.Background())
	runner := &scriptedDNSCommandRunner{
		combinedOutput: func(ctx context.Context, name string, _ ...string) ([]byte, error) {
			if name != "dscacheutil" {
				return nil, fmt.Errorf("unexpected command %s", name)
			}
			cancel()
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}

	_, err := disableDNSDarwinContextWithRunner(ctx, runner, statePath, "")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("DisableDNS error = %v, want context canceled", err)
	}
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("DNS retry state removed after cache cancellation: %v", err)
	}
}

func TestDisableDNSDarwinContextRetainsStateWhenCacheFlushFails(t *testing.T) {
	for _, tc := range []struct {
		name    string
		command string
	}{
		{name: "dscacheutil", command: "dscacheutil"},
		{name: "mDNSResponder", command: "killall"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			statePath := writeTestDNSState(t, dnsState{Service: "Wi-Fi", Servers: []string{"1.1.1.1"}})
			flushErr := errors.New(tc.name + " failed")
			runner := &scriptedDNSCommandRunner{
				combinedOutput: func(_ context.Context, name string, _ ...string) ([]byte, error) {
					if name == tc.command {
						return nil, flushErr
					}
					return nil, nil
				},
			}

			_, err := disableDNSDarwinContextWithRunner(context.Background(), runner, statePath, "")
			if !errors.Is(err, flushErr) {
				t.Fatalf("DisableDNS error = %v, want %v", err, flushErr)
			}
			if _, err := os.Stat(statePath); err != nil {
				t.Fatalf("DNS retry state removed after %s failure: %v", tc.name, err)
			}
		})
	}
}

func TestFlushDNSCacheContextWithRunnerSkipsUnavailableCommandOnlyWhenAlternativeSucceeds(t *testing.T) {
	for _, tc := range []struct {
		name        string
		unavailable map[string]bool
		wantErr     bool
	}{
		{
			name:        "dscacheutil unavailable",
			unavailable: map[string]bool{"dscacheutil": true},
		},
		{
			name:        "killall unavailable",
			unavailable: map[string]bool{"killall": true},
		},
		{
			name: "all unavailable",
			unavailable: map[string]bool{
				"dscacheutil": true,
				"killall":     true,
			},
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &scriptedDNSCommandRunner{
				combinedOutput: func(_ context.Context, name string, _ ...string) ([]byte, error) {
					if tc.unavailable[name] {
						return nil, exec.ErrNotFound
					}
					return nil, nil
				},
			}

			err := flushDNSCacheContextWithRunner(context.Background(), runner)
			if tc.wantErr && err == nil {
				t.Fatal("flushDNSCache accepted no available cache flush command")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("flushDNSCache error = %v, want nil", err)
			}
		})
	}
}

func TestDisableDNSDarwinContextRetainsStateWhenFinalInspectionFails(t *testing.T) {
	statePath := writeTestDNSState(t, dnsState{Service: "Wi-Fi", Servers: []string{"1.1.1.1"}})
	inspectErr := errors.New("networksetup inspection failed")
	runner := &scriptedDNSCommandRunner{
		combinedOutput: func(_ context.Context, name string, _ ...string) ([]byte, error) {
			if name == "networksetup" {
				return nil, inspectErr
			}
			return nil, nil
		},
	}

	_, err := disableDNSDarwinContextWithRunner(context.Background(), runner, statePath, "")
	if !errors.Is(err, inspectErr) {
		t.Fatalf("DisableDNS error = %v, want inspection failure", err)
	}
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("DNS retry state removed after inspection failure: %v", err)
	}
}

func TestDisableDNSDarwinContextRetainsStateWhenRestoredServersMismatch(t *testing.T) {
	statePath := writeTestDNSState(t, dnsState{Service: "Wi-Fi", Servers: []string{"1.1.1.1"}})
	runner := &scriptedDNSCommandRunner{
		combinedOutput: func(_ context.Context, name string, args ...string) ([]byte, error) {
			if name == "networksetup" && strings.Join(args, " ") == "-getdnsservers Wi-Fi" {
				return []byte("8.8.8.8\n"), nil
			}
			return nil, nil
		},
	}

	if _, err := disableDNSDarwinContextWithRunner(context.Background(), runner, statePath, ""); err == nil {
		t.Fatal("DisableDNS accepted restored servers that do not match durable state")
	}
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("DNS retry state removed after verification mismatch: %v", err)
	}
}

func TestDisableDNSDarwinContextRemovesStateOnlyAfterVerifiedSuccess(t *testing.T) {
	statePath := writeTestDNSState(t, dnsState{Service: "Wi-Fi", Servers: []string{"1.1.1.1"}})
	runner := &scriptedDNSCommandRunner{
		combinedOutput: func(_ context.Context, name string, args ...string) ([]byte, error) {
			if name == "networksetup" && strings.Join(args, " ") == "-getdnsservers Wi-Fi" {
				return []byte("1.1.1.1\n"), nil
			}
			return nil, nil
		},
	}

	status, err := disableDNSDarwinContextWithRunner(context.Background(), runner, statePath, "")
	if err != nil {
		t.Fatal(err)
	}
	if status.Enabled || status.StateSaved {
		t.Fatalf("restored DNS status = %+v, want disabled with no saved state", status)
	}
	if _, err := os.Stat(statePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("DNS retry state error = %v, want removed after verification", err)
	}
}

func writeTestDNSState(t *testing.T, state dnsState) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dns-original.json")
	b, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

type scriptedDNSCommandRunner struct {
	combinedOutput func(context.Context, string, ...string) ([]byte, error)
}

func (r *scriptedDNSCommandRunner) CombinedOutput(ctx context.Context, name string, args ...string) ([]byte, error) {
	if r.combinedOutput == nil {
		return nil, nil
	}
	return r.combinedOutput(ctx, name, args...)
}

func (*scriptedDNSCommandRunner) Run(context.Context, string, ...string) error {
	return nil
}

type blockingDNSCommandRunner struct {
	entered chan struct{}
}

func (*blockingDNSCommandRunner) CombinedOutput(context.Context, string, ...string) ([]byte, error) {
	return nil, errors.New("unexpected CombinedOutput")
}

func (r *blockingDNSCommandRunner) Run(ctx context.Context, _ string, _ ...string) error {
	close(r.entered)
	<-ctx.Done()
	return ctx.Err()
}

func TestUnitInstalledFalseWhenAbsent(t *testing.T) {
	// 只验证函数可调用且返回 bool(系统服务在测试环境状态不定)
	_ = UnitInstalled()
}

func TestCopyExecutable(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src-bx")
	want := []byte("#!/bin/sh\necho bx\n")
	if err := os.WriteFile(src, want, 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "sub", "bx") // 父目录不存在,验证 MkdirAll
	if err := copyExecutable(src, dst); err != nil {
		t.Fatalf("copyExecutable: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("内容不一致: %q != %q", got, want)
	}
	fi, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o755 {
		t.Fatalf("权限: %o != 0755", fi.Mode().Perm())
	}
}
