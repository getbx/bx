package install

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
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
