//go:build darwin

package install

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/release"
)

func writeFakeBundle(t *testing.T, dir string, version string) (bundle string, cli, bridgeBin []byte) {
	t.Helper()
	cli = []byte("cli-" + version)
	bridgeBin = []byte("bridge-" + version)
	bundle = filepath.Join(dir, "Bx.app")
	resources := filepath.Join(bundle, "Contents", "Resources")
	macos := filepath.Join(bundle, "Contents", "MacOS")
	for _, d := range []string{resources, macos} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.WriteFile(filepath.Join(macos, "BxMenu"), []byte("menu"), 0o755))
	must(os.WriteFile(filepath.Join(bundle, "Contents", "Info.plist"),
		[]byte(`<plist><dict><key>CFBundleIdentifier</key><string>com.getbx.bx.menu</string></dict></plist>`), 0o644))
	must(os.WriteFile(filepath.Join(resources, "bx-cli"), cli, 0o755))
	must(os.WriteFile(filepath.Join(resources, "bx-bridge"), bridgeBin, 0o755))
	cliSum, bridgeSum := sha256.Sum256(cli), sha256.Sum256(bridgeBin)
	must(release.Write(filepath.Join(resources, release.FileName), release.Info{
		SchemaVersion: release.SchemaVersion, Version: version,
		Platform: runtime.GOOS + "/" + runtime.GOARCH,
		Assets: map[string]string{
			"bx-cli":    hex.EncodeToString(cliSum[:]),
			"bx-bridge": hex.EncodeToString(bridgeSum[:]),
		},
	}))
	return bundle, cli, bridgeBin
}

func testOptions(t *testing.T, bundle string) (UnifiedInstallOptions, *[]string) {
	t.Helper()
	opts, guardianCalls, _ := testOptionsWithChown(t, bundle)
	return opts, guardianCalls
}

// testOptionsWithChown 同 testOptions,额外返回一个记录所有 Chown 调用("path uid:gid")的切片指针,
// 供需要断言 chown 实际发生的用例使用。
func testOptionsWithChown(t *testing.T, bundle string) (UnifiedInstallOptions, *[]string, *[]string) {
	t.Helper()
	base := t.TempDir()
	var guardianCalls []string
	var chownCalls []string
	opts := UnifiedInstallOptions{
		BundlePath:     bundle,
		AppDestination: filepath.Join(base, "Applications", "Bx.app"),
		ConfigPath:     "/etc/bx/config.yaml",
		RuntimeRoot:    filepath.Join(base, "runtime"),
		BridgePath:     filepath.Join(base, "bin", "bx"),
		ConsoleHome:    filepath.Join(base, "home"),
		ConsoleUID:     501, ConsoleGID: 20,
		Chown: func(path string, uid, gid int) error {
			chownCalls = append(chownCalls, fmt.Sprintf("%s %d:%d", path, uid, gid))
			return nil
		},
		WriteGuardian: func(executable, configPath string) error {
			guardianCalls = append(guardianCalls, executable+" "+configPath)
			return nil
		},
	}
	if err := os.MkdirAll(filepath.Dir(opts.BridgePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(opts.AppDestination), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(opts.ConsoleHome, 0o755); err != nil {
		t.Fatal(err)
	}
	return opts, &guardianCalls, &chownCalls
}

func TestUnifiedInstallFresh(t *testing.T) {
	bundle, cli, bridgeBin := writeFakeBundle(t, t.TempDir(), "1.0.0")
	opts, guardianCalls, chownCalls := testOptionsWithChown(t, bundle)
	result, err := UnifiedInstall(opts)
	if err != nil {
		t.Fatalf("UnifiedInstall: %v", err)
	}
	if result.Version != "1.0.0" || !result.BridgeReplaced {
		t.Fatalf("result = %+v", result)
	}
	gotCLI, _ := os.ReadFile(filepath.Join(opts.RuntimeRoot, "current", "bx"))
	if string(gotCLI) != string(cli) {
		t.Fatal("runtime cli mismatch")
	}
	gotBridge, _ := os.ReadFile(opts.BridgePath)
	if string(gotBridge) != string(bridgeBin) {
		t.Fatal("bridge mismatch")
	}
	if _, err := os.Stat(filepath.Join(opts.AppDestination, "Contents", "MacOS", "BxMenu")); err != nil {
		t.Fatalf("app not installed: %v", err)
	}
	agent := filepath.Join(opts.ConsoleHome, "Library", "LaunchAgents", "com.getbx.bx.menu.plist")
	agentText, err := os.ReadFile(agent)
	if err != nil || !strings.Contains(string(agentText), opts.AppDestination+"/Contents/MacOS/BxMenu") {
		t.Fatalf("agent plist: %v %q", err, agentText)
	}
	if len(*guardianCalls) != 1 || !strings.Contains((*guardianCalls)[0], filepath.Join(opts.RuntimeRoot, "current", "bx")) {
		t.Fatalf("guardian calls = %v", *guardianCalls)
	}

	// Chown 必须真的被调用到:staged App 树(以 root:wheel 落位)、bridge 二进制、agent plist(以
	// 控制台用户 uid:gid 落位)——防止「静默从不 chown,文件其实以 build 时的 uid 落地」这类回归。
	calls := *chownCalls
	appTreeChowned := false
	for _, c := range calls {
		if strings.HasSuffix(c, "/Contents/MacOS/BxMenu 0:0") {
			appTreeChowned = true
			break
		}
	}
	if !appTreeChowned {
		t.Fatalf("want a chown call covering staged .../Contents/MacOS/BxMenu at 0:0, got %v", calls)
	}
	bridgeWant := opts.BridgePath + " 0:0"
	bridgeChowned := false
	for _, c := range calls {
		if c == bridgeWant {
			bridgeChowned = true
			break
		}
	}
	if !bridgeChowned {
		t.Fatalf("want chown call %q, got %v", bridgeWant, calls)
	}
	agentWant := fmt.Sprintf("%s %d:%d", agent, opts.ConsoleUID, opts.ConsoleGID)
	agentChowned := false
	for _, c := range calls {
		if c == agentWant {
			agentChowned = true
			break
		}
	}
	if !agentChowned {
		t.Fatalf("want chown call %q, got %v", agentWant, calls)
	}
}

func TestUnifiedInstallIdempotent(t *testing.T) {
	bundle, _, _ := writeFakeBundle(t, t.TempDir(), "1.0.0")
	opts, _ := testOptions(t, bundle)
	if _, err := UnifiedInstall(opts); err != nil {
		t.Fatal(err)
	}
	result, err := UnifiedInstall(opts)
	if err != nil {
		t.Fatalf("second install: %v", err)
	}
	if result.BridgeReplaced {
		t.Fatal("unchanged bridge must not be replaced")
	}
}

func TestUnifiedInstallDigestMismatchTouchesNothing(t *testing.T) {
	dir := t.TempDir()
	bundle, _, _ := writeFakeBundle(t, dir, "1.0.0")
	// 篡改 bx-cli,digest 不再匹配
	if err := os.WriteFile(filepath.Join(bundle, "Contents", "Resources", "bx-cli"), []byte("tampered"), 0o755); err != nil {
		t.Fatal(err)
	}
	opts, guardianCalls := testOptions(t, bundle)
	if _, err := UnifiedInstall(opts); err == nil {
		t.Fatal("want digest error")
	}
	if _, err := os.Stat(opts.RuntimeRoot); !os.IsNotExist(err) {
		t.Fatal("runtime must be untouched")
	}
	if _, err := os.Stat(opts.BridgePath); !os.IsNotExist(err) {
		t.Fatal("bridge must be untouched")
	}
	if _, err := os.Stat(opts.AppDestination); !os.IsNotExist(err) {
		t.Fatal("app must be untouched")
	}
	if len(*guardianCalls) != 0 {
		t.Fatal("guardian plist must be untouched")
	}
}

func TestUnifiedInstallMigratesLegacyUserApp(t *testing.T) {
	bundle, _, _ := writeFakeBundle(t, t.TempDir(), "1.0.0")
	opts, _ := testOptions(t, bundle)
	legacy := filepath.Join(opts.ConsoleHome, "Applications", "Bx.app", "Contents")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "Info.plist"),
		[]byte(`<plist><dict><key>CFBundleIdentifier</key><string>com.getbx.bx.menu</string></dict></plist>`), 0o644); err != nil {
		t.Fatal(err)
	}
	legacyAgent := filepath.Join(opts.ConsoleHome, "Library", "LaunchAgents", "com.ggshr9.bx.menu.plist")
	if err := os.MkdirAll(filepath.Dir(legacyAgent), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyAgent, []byte("legacy"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := UnifiedInstall(opts)
	if err != nil {
		t.Fatal(err)
	}
	if !result.RemovedLegacyApp {
		t.Fatal("want legacy app removed")
	}
	if _, err := os.Stat(filepath.Join(opts.ConsoleHome, "Applications", "Bx.app")); !os.IsNotExist(err) {
		t.Fatal("legacy app still present")
	}
	if _, err := os.Stat(legacyAgent); !os.IsNotExist(err) {
		t.Fatal("legacy agent plist still present")
	}
}

func TestUnifiedInstallKeepsForeignUserApp(t *testing.T) {
	bundle, _, _ := writeFakeBundle(t, t.TempDir(), "1.0.0")
	opts, _ := testOptions(t, bundle)
	foreign := filepath.Join(opts.ConsoleHome, "Applications", "Bx.app", "Contents")
	if err := os.MkdirAll(foreign, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(foreign, "Info.plist"),
		[]byte(`<plist><dict><key>CFBundleIdentifier</key><string>com.example.other</string></dict></plist>`), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := UnifiedInstall(opts)
	if err != nil {
		t.Fatal(err)
	}
	if result.RemovedLegacyApp {
		t.Fatal("foreign bundle id must not be removed")
	}
}
