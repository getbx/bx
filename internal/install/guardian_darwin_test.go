//go:build darwin

package install

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGuardianPlistTextUsesCanonicalLifecycleOwner(t *testing.T) {
	plist := GuardianPlistText("/etc/bx/config.yaml")
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
	commands := guardianEnableCommands(false)
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
	if commands := guardianEnableCommands(true); len(commands) != 0 {
		t.Fatalf("active Guardian should need no commands: %#v", commands)
	}
}

func TestGuardianConfigPathFromExecStartRejectsDirectCore(t *testing.T) {
	got, err := guardianConfigPathFromExecStart("/usr/local/bin/bx guardian --config /etc/bx/config.yaml --listen-dns 127.0.0.1:53")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/etc/bx/config.yaml" {
		t.Fatalf("Guardian config path = %q", got)
	}
	if _, err := guardianConfigPathFromExecStart("/usr/local/bin/bx run -c /etc/bx/config.yaml --listen-dns 127.0.0.1:53"); err == nil {
		t.Fatal("fresh macOS setup accepted a direct Core service")
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
	err := writeGuardianUnitAt(path, "/etc/bx/config.yaml", func(path string, uid, gid int) error {
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
	if err := enableGuardianWithControl(context.Background(), control); err != nil {
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
