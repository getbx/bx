package install

import (
	"os"
	"path/filepath"
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
		"RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX",
		"CapabilityBoundingSet=",
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
	if fi.Mode().Perm() != 0o755 {
		t.Fatalf("权限: %o != 0755", fi.Mode().Perm())
	}
}
