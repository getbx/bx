package runtimedir

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/getbx/bx/internal/release"
)

func payloadFor(t *testing.T, version string, cli []byte) Payload {
	t.Helper()
	sum := sha256.Sum256(cli)
	return Payload{CLI: cli, Info: release.Info{
		SchemaVersion: release.SchemaVersion,
		Version:       version,
		Platform:      "darwin/arm64",
		Assets:        map[string]string{"bx-cli": hex.EncodeToString(sum[:])},
	}}
}

func TestInstallSwitchCurrent(t *testing.T) {
	root := filepath.Join(t.TempDir(), "runtime")
	dir, err := InstallVersion(root, payloadFor(t, "1.0.0", []byte("cli-v1")))
	if err != nil {
		t.Fatalf("InstallVersion: %v", err)
	}
	if dir != filepath.Join(root, "1.0.0") {
		t.Fatalf("version dir = %q", dir)
	}
	if Installed(root) {
		t.Fatal("Installed should be false before SwitchCurrent")
	}
	if err := SwitchCurrent(root, "1.0.0"); err != nil {
		t.Fatalf("SwitchCurrent: %v", err)
	}
	if !Installed(root) {
		t.Fatal("Installed should be true")
	}
	info, versionDir, err := Current(root)
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if info.Version != "1.0.0" || versionDir != dir {
		t.Fatalf("Current = %q %q", info.Version, versionDir)
	}
	data, err := os.ReadFile(ExecutablePath(root))
	if err != nil || string(data) != "cli-v1" {
		t.Fatalf("executable content: %q err=%v", data, err)
	}
	fi, err := os.Stat(filepath.Join(dir, "bx"))
	if err != nil || fi.Mode().Perm() != 0o755 {
		t.Fatalf("bx perm = %v err=%v", fi.Mode(), err)
	}
	dirFi, err := os.Stat(dir)
	if err != nil || dirFi.Mode().Perm() != 0o755 {
		t.Fatalf("version dir perm = %v err=%v", dirFi.Mode(), err)
	}
	releaseFi, err := os.Stat(filepath.Join(dir, release.FileName))
	if err != nil || releaseFi.Mode().Perm() != 0o644 {
		t.Fatalf("release.json perm = %v err=%v", releaseFi.Mode(), err)
	}
}

func TestSwitchCurrentReplacesAtomically(t *testing.T) {
	root := filepath.Join(t.TempDir(), "runtime")
	for _, v := range []string{"1.0.0", "1.1.0"} {
		if _, err := InstallVersion(root, payloadFor(t, v, []byte("cli-"+v))); err != nil {
			t.Fatalf("install %s: %v", v, err)
		}
	}
	if err := SwitchCurrent(root, "1.0.0"); err != nil {
		t.Fatal(err)
	}
	if err := SwitchCurrent(root, "1.1.0"); err != nil {
		t.Fatalf("re-switch: %v", err)
	}
	target, err := os.Readlink(CurrentLink(root))
	if err != nil || target != "1.1.0" {
		t.Fatalf("current -> %q err=%v", target, err)
	}
}

func TestInstallVersionIdempotentAndReplacing(t *testing.T) {
	root := filepath.Join(t.TempDir(), "runtime")
	p := payloadFor(t, "1.0.0", []byte("same"))
	if _, err := InstallVersion(root, p); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallVersion(root, p); err != nil {
		t.Fatalf("idempotent reinstall: %v", err)
	}
	changed := payloadFor(t, "1.0.0", []byte("different-bytes"))
	if _, err := InstallVersion(root, changed); err != nil {
		t.Fatalf("replacing reinstall: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(root, "1.0.0", "bx"))
	if string(data) != "different-bytes" {
		t.Fatalf("content not replaced: %q", data)
	}
}

func TestInstallVersionRejects(t *testing.T) {
	root := filepath.Join(t.TempDir(), "runtime")
	bad := payloadFor(t, "1.0.0", []byte("cli"))
	bad.Info.Assets["bx-cli"] = hex.EncodeToString(make([]byte, sha256.Size)) // digest 不匹配
	if _, err := InstallVersion(root, bad); err == nil {
		t.Fatal("want digest mismatch error")
	}
	evil := payloadFor(t, "../evil", []byte("cli"))
	if _, err := InstallVersion(root, evil); err == nil {
		t.Fatal("want version path error")
	}
	if err := SwitchCurrent(root, "missing"); err == nil {
		t.Fatal("want missing version error")
	}
}

func TestCurrentRejectsAbsoluteOrEscapingLink(t *testing.T) {
	root := filepath.Join(t.TempDir(), "runtime")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc", CurrentLink(root)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Current(root); err == nil {
		t.Fatal("want escaping link rejected")
	}
}
