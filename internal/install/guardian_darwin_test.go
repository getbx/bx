//go:build darwin

package install

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/runtimedir"
)

func TestGuardianPlistTextUsesCanonicalLifecycleOwner(t *testing.T) {
	plist := GuardianPlistText(BinPath, "/etc/bx/config.yaml")
	for _, want := range []string{
		"<string>com.getbx.bx.guard</string>",
		"<string>/usr/local/bin/bx</string>",
		"<string>guardian</string>",
		"<string>--config</string>",
		"<string>/etc/bx/config.yaml</string>",
		"<string>--listen-dns</string>",
		"<string>127.0.0.1:53</string>",
		"<key>RunAtLoad</key>\n  <true/>",
		"<key>KeepAlive</key>\n  <true/>",
		"<key>UserName</key>\n  <string>root</string>",
		"<key>GroupName</key>\n  <string>wheel</string>",
		"<string>/var/log/bx-guard.log</string>",
		"<string>/var/log/bx-guard.err.log</string>",
	} {
		if !strings.Contains(plist, want) {
			t.Errorf("Guardian plist missing %q:\n%s", want, plist)
		}
	}
	for _, forbidden := range []string{"<string>run</string>", "client_link", "server_link"} {
		if strings.Contains(plist, forbidden) {
			t.Errorf("Guardian plist contains forbidden value %q", forbidden)
		}
	}
}

func TestGuardianEnableCommandsBootstrapCanonicalLabel(t *testing.T) {
	commands := guardianEnableCommands(false, false)
	want := []string{
		"enable system/com.getbx.bx.guard",
		"bootstrap system /Library/LaunchDaemons/com.getbx.bx.guard.plist",
		"kickstart -k system/com.getbx.bx.guard",
	}
	if len(commands) != len(want) {
		t.Fatalf("guardianEnableCommands len = %d, want %d", len(commands), len(want))
	}
	for i := range want {
		if got := strings.Join(commands[i], " "); got != want[i] {
			t.Fatalf("command[%d] = %q, want %q", i, got, want[i])
		}
	}
	if commands := guardianEnableCommands(true, true); len(commands) != 0 {
		t.Fatalf("active+ready Guardian should need no commands: %#v", commands)
	}
}

func TestGuardianEnableCommandsKickstartsLoadedButNotReady(t *testing.T) {
	if got := guardianEnableCommands(true, true); got != nil {
		t.Fatalf("loaded+ready 应为 no-op,got %v", got)
	}
	got := guardianEnableCommands(true, false)
	want := [][]string{{"kickstart", "-k", "system/com.getbx.bx.guard"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("loaded+not-ready = %v want %v", got, want)
	}
	if len(guardianEnableCommands(false, false)) != 3 {
		t.Fatal("未加载时仍应 enable+bootstrap+kickstart")
	}
}

func TestGuardianConfigPathFromExecStartRejectsDirectCore(t *testing.T) {
	gotExec, got, err := parseGuardianExecStart("/usr/local/bin/bx guardian --config /etc/bx/config.yaml --listen-dns 127.0.0.1:53")
	if err != nil {
		t.Fatal(err)
	}
	if gotExec != "/usr/local/bin/bx" {
		t.Fatalf("Guardian executable = %q", gotExec)
	}
	if got != "/etc/bx/config.yaml" {
		t.Fatalf("Guardian config path = %q", got)
	}
	if _, _, err := parseGuardianExecStart("/usr/local/bin/bx run -c /etc/bx/config.yaml --listen-dns 127.0.0.1:53"); err == nil {
		t.Fatal("fresh macOS setup accepted a direct Core service")
	}
}

func TestGuardianPlistTextRuntimeExecutable(t *testing.T) {
	text := GuardianPlistText("/Library/Application Support/bx/runtime/current/bx", "/etc/bx/config.yaml")
	for _, want := range []string{
		"<string>/Library/Application Support/bx/runtime/current/bx</string>",
		"<string>guardian</string>",
		"<string>/etc/bx/config.yaml</string>",
		"<string>com.getbx.bx.guard</string>",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("plist missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "<string>/usr/local/bin/bx</string>") {
		t.Fatal("runtime plist must not reference the bridge path")
	}
}

func TestParseGuardianExecStart(t *testing.T) {
	runtimeExec := runtimedir.ExecutablePath(runtimedir.Root)
	cases := []struct{ execStart, wantExec, wantConfig string }{
		{BinPath + " guardian --config /etc/bx/config.yaml --listen-dns 127.0.0.1:53", BinPath, "/etc/bx/config.yaml"},
		{runtimeExec + " guardian --config /etc/bx/config.yaml --listen-dns 127.0.0.1:53", runtimeExec, "/etc/bx/config.yaml"},
	}
	for _, tc := range cases {
		gotExec, gotConfig, err := parseGuardianExecStart(tc.execStart)
		if err != nil || gotExec != tc.wantExec || gotConfig != tc.wantConfig {
			t.Fatalf("parse(%q) = %q %q %v", tc.execStart, gotExec, gotConfig, err)
		}
	}
	if _, _, err := parseGuardianExecStart("/opt/evil/bx guardian --config /etc/bx/config.yaml --listen-dns 127.0.0.1:53"); err == nil {
		t.Fatal("want unknown executable rejected")
	}
}

func TestLegacyCoreBootoutCommandsTreatAbsentOrDisabledLabelsAsSuccess(t *testing.T) {
	if commands := legacyCoreBootoutCommands(nil); len(commands) != 0 {
		t.Fatalf("absent labels should need no commands: %#v", commands)
	}
	if commands := legacyCoreBootoutCommands(map[string]bool{
		"com.getbx.bx":  false,
		"com.ggshr9.bx": false,
	}); len(commands) != 0 {
		t.Fatalf("disabled labels should need no commands: %#v", commands)
	}
}

func TestLegacyCoreBootoutCommandsOnlyStopLoadedDirectCore(t *testing.T) {
	commands := legacyCoreBootoutCommands(map[string]bool{
		"com.getbx.bx":  true,
		"com.ggshr9.bx": false,
	})
	if len(commands) != 1 {
		t.Fatalf("legacyCoreBootoutCommands = %#v", commands)
	}
	if got, want := strings.Join(commands[0], " "), "bootout system/com.getbx.bx"; got != want {
		t.Fatalf("bootout command = %q, want %q", got, want)
	}
}

func TestLegacyCoreInstalledAtRecognizesEitherDirectPlist(t *testing.T) {
	dir := t.TempDir()
	paths := []string{
		filepath.Join(dir, "com.getbx.bx.plist"),
		filepath.Join(dir, "com.ggshr9.bx.plist"),
	}
	if legacyCoreInstalledAt(paths) {
		t.Fatal("missing direct Core plists reported installed")
	}
	if err := os.WriteFile(paths[1], []byte("plist"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !legacyCoreInstalledAt(paths) {
		t.Fatal("legacy direct Core plist was not detected")
	}
}

func TestWriteGuardianUnitAtEnforcesRootOwnershipAndMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "com.getbx.bx.guard.plist")
	if err := os.WriteFile(path, []byte("stale"), 0o666); err != nil {
		t.Fatal(err)
	}
	var ownerPath string
	var ownerUID, ownerGID int
	err := writeGuardianUnitAt(path, BinPath, "/etc/bx/config.yaml", func(path string, uid, gid int) error {
		ownerPath, ownerUID, ownerGID = path, uid, gid
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Fatalf("Guardian plist mode = %#o, want 0644", got)
	}
	if ownerPath != path || ownerUID != 0 || ownerGID != 0 {
		t.Fatalf("Guardian plist ownership = (%q, %d, %d), want (%q, 0, 0)", ownerPath, ownerUID, ownerGID, path)
	}
}

func TestEnableGuardianWithControlUsesPlannedArgv(t *testing.T) {
	control := &fakeGuardianLaunchdControl{}
	if err := enableGuardianWithControl(context.Background(), control, func() bool { return false }); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"enable system/com.getbx.bx.guard",
		"bootstrap system /Library/LaunchDaemons/com.getbx.bx.guard.plist",
		"kickstart -k system/com.getbx.bx.guard",
	}
	if strings.Join(control.calls, "|") != strings.Join(want, "|") {
		t.Fatalf("launchctl calls = %#v, want %#v", control.calls, want)
	}
}

func TestEnableGuardianWithControlKickstartsWhenLoadedButProbeUnready(t *testing.T) {
	control := &fakeGuardianLaunchdControl{loaded: map[string]bool{guardianLaunchdLabel: true}}
	if err := enableGuardianWithControl(context.Background(), control, func() bool { return false }); err != nil {
		t.Fatal(err)
	}
	want := []string{"kickstart -k system/com.getbx.bx.guard"}
	if strings.Join(control.calls, "|") != strings.Join(want, "|") {
		t.Fatalf("launchctl calls = %#v, want %#v", control.calls, want)
	}
}

func TestEnableGuardianWithControlNoopWhenLoadedAndReady(t *testing.T) {
	control := &fakeGuardianLaunchdControl{loaded: map[string]bool{guardianLaunchdLabel: true}}
	if err := enableGuardianWithControl(context.Background(), control, func() bool { return true }); err != nil {
		t.Fatal(err)
	}
	if len(control.calls) != 0 {
		t.Fatalf("loaded+ready should be a no-op: %#v", control.calls)
	}
}

func TestEnableGuardianWithControlTreatsNilProbeAsNotReady(t *testing.T) {
	control := &fakeGuardianLaunchdControl{loaded: map[string]bool{guardianLaunchdLabel: true}}
	if err := enableGuardianWithControl(context.Background(), control, nil); err != nil {
		t.Fatal(err)
	}
	want := []string{"kickstart -k system/com.getbx.bx.guard"}
	if strings.Join(control.calls, "|") != strings.Join(want, "|") {
		t.Fatalf("nil probe should still kickstart: %#v", control.calls)
	}
}

func TestBootoutGuardianWithControlStopsLoadedServiceOnly(t *testing.T) {
	control := &fakeGuardianLaunchdControl{loaded: map[string]bool{guardianLaunchdLabel: true}}
	if err := bootoutGuardianWithControl(context.Background(), control); err != nil {
		t.Fatal(err)
	}
	want := []string{"bootout system/com.getbx.bx.guard"}
	if strings.Join(control.calls, "|") != strings.Join(want, "|") {
		t.Fatalf("launchctl calls = %#v, want %#v", control.calls, want)
	}
}

func TestBootoutGuardianWithControlNoopWhenNotLoaded(t *testing.T) {
	control := &fakeGuardianLaunchdControl{}
	if err := bootoutGuardianWithControl(context.Background(), control); err != nil {
		t.Fatal(err)
	}
	if len(control.calls) != 0 {
		t.Fatalf("bootout issued for a service that was never loaded: %#v", control.calls)
	}
}

func TestBootoutGuardianWithControlToleratesRaceWhereServiceAlreadyExited(t *testing.T) {
	// bootout errored, but a follow-up Loaded() probe shows the service is
	// gone anyway (e.g. it exited between the two launchctl calls): this
	// must not be surfaced as a failure to the caller (bx down must
	// succeed rather than block the user from escaping).
	control := &raceGuardianLaunchdControl{
		runErr: errors.New("no such process"),
	}
	if err := bootoutGuardianWithControl(context.Background(), control); err != nil {
		t.Fatalf("bootout race treated as failure: %v", err)
	}
}

func TestBootoutGuardianWithControlReportsGenuineFailure(t *testing.T) {
	control := &fakeGuardianLaunchdControl{
		loaded: map[string]bool{guardianLaunchdLabel: true},
		runErr: map[string]error{"bootout system/com.getbx.bx.guard": errors.New("permission denied")},
	}
	if err := bootoutGuardianWithControl(context.Background(), control); err == nil {
		t.Fatal("genuine bootout failure (service still loaded) was swallowed")
	}
}

// raceGuardianLaunchdControl simulates a Guardian service that reports
// loaded on the pre-Run probe, has its Run("bootout", ...) call error, but
// is observed gone on the post-Run probe — exercising the "did it actually
// stop despite Run erroring" fallback check.
type raceGuardianLaunchdControl struct {
	runErr error
	ran    bool
}

func (r *raceGuardianLaunchdControl) Loaded(context.Context, string) (bool, error) {
	return !r.ran, nil
}

func (r *raceGuardianLaunchdControl) Run(context.Context, ...string) error {
	r.ran = true
	return r.runErr
}

func TestGuardianLogTailReturnsLastLinesAndEmptyWhenUnreadable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bx-guard.err.log")
	content := strings.Join([]string{"line1", "line2", "line3", "line4"}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, want := guardianLogTailAt(path, 2), "line3\nline4"; got != want {
		t.Fatalf("guardianLogTailAt = %q, want %q", got, want)
	}
	if got := guardianLogTailAt(filepath.Join(dir, "missing.log"), 10); got != "" {
		t.Fatalf("missing log should be empty, got %q", got)
	}
}

func TestBootoutLegacyCoreWithControlTreatsAbsentLabelsAsSuccess(t *testing.T) {
	control := &fakeGuardianLaunchdControl{}
	if err := bootoutLegacyCoreWithControl(context.Background(), control); err != nil {
		t.Fatal(err)
	}
	if len(control.calls) != 0 {
		t.Fatalf("absent labels caused launchctl mutations: %#v", control.calls)
	}
}

func TestBootoutLegacyCoreWithControlFailsBeforeMutationOnAmbiguousStatus(t *testing.T) {
	control := &fakeGuardianLaunchdControl{
		loaded:    map[string]bool{launchdLabel: true},
		statusErr: map[string]error{legacyLaunchdLabel: errors.New("launchd unavailable")},
	}
	if err := bootoutLegacyCoreWithControl(context.Background(), control); err == nil {
		t.Fatal("ambiguous legacy status accepted")
	}
	if len(control.calls) != 0 {
		t.Fatalf("status failure must precede every bootout: %#v", control.calls)
	}
}

func TestRemoveLegacyCoreUnitWithDepsDeletesOnlyInactivePlists(t *testing.T) {
	dir := t.TempDir()
	current := filepath.Join(dir, "com.getbx.bx.plist")
	legacy := filepath.Join(dir, "com.ggshr9.bx.plist")
	for _, path := range []string{current, legacy} {
		if err := os.WriteFile(path, []byte("plist"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	control := &fakeGuardianLaunchdControl{}
	if err := removeLegacyCoreUnitWithDeps(context.Background(), control, []string{current, legacy}, os.Remove); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{current, legacy} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("legacy plist retained at %s: %v", path, err)
		}
	}

	control.loaded = map[string]bool{launchdLabel: true}
	if err := os.WriteFile(current, []byte("plist"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := removeLegacyCoreUnitWithDeps(context.Background(), control, []string{current}, os.Remove); err == nil {
		t.Fatal("loaded direct Core plist was deleted")
	}
	if _, err := os.Stat(current); err != nil {
		t.Fatalf("loaded direct Core plist was mutated: %v", err)
	}
}

type fakeGuardianLaunchdControl struct {
	loaded    map[string]bool
	statusErr map[string]error
	runErr    map[string]error
	calls     []string
}

func (f *fakeGuardianLaunchdControl) Loaded(_ context.Context, label string) (bool, error) {
	if err := f.statusErr[label]; err != nil {
		return false, err
	}
	return f.loaded[label], nil
}

func (f *fakeGuardianLaunchdControl) Run(_ context.Context, args ...string) error {
	call := strings.Join(args, " ")
	f.calls = append(f.calls, call)
	return f.runErr[call]
}

// Guardian 日志里有服务器 IP、bypass 网段和完整错误串(可能含路径/链接/凭据)。
// 真机实测 launchd 建出来的是 0644 —— 任何本地用户都能读。安装时必须收紧到 0600。
func TestSecureGuardianLogsAtCreatesRootOnlyFiles(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "bx-guard.err.log")
	if err := os.WriteFile(existing, []byte("166.1.190.123\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "bx-guard.log")

	owners := map[string][2]int{}
	err := secureGuardianLogsAt([]string{existing, missing}, func(path string, uid, gid int) error {
		owners[path] = [2]int{uid, gid}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{existing, missing} {
		info, statErr := os.Stat(path)
		if statErr != nil {
			t.Fatalf("Guardian 日志必须存在(不存在就创建):%v", statErr)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Errorf("%s mode = %#o, want 0600", path, got)
		}
		if owners[path] != [2]int{0, 0} {
			t.Errorf("%s owner = %v, want root:wheel(0,0)", path, owners[path])
		}
	}
	// 收紧权限不能顺手清空已有日志——那等于销毁排查材料。
	content, err := os.ReadFile(existing)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "166.1.190.123") {
		t.Errorf("已有日志内容必须保留,实际 = %q", content)
	}
}

// 安装 Guardian 单元时顺带收紧日志权限,不能只在某条罕见路径上做。
func TestGuardianLogPathsCoverBothLaunchdLogs(t *testing.T) {
	paths := GuardianLogPaths()
	want := []string{"/var/log/bx-guard.log", "/var/log/bx-guard.err.log"}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("GuardianLogPaths() = %v, want %v", paths, want)
	}
}
