package cli

import (
	"os"
	"path/filepath"
	"testing"

	updatepkg "github.com/getbx/bx/internal/update"
)

// 直装那条路(保护关着时走的 writeMacOSAppTree)与 Guardian 主持的那条
// (update.stageApp)必须给出**同一组权限位**。
//
// 两个写者各写一份清单,是 2026-09-18 那个真机 bug 的整个形状:窄的那份漏了
// `Contents/Resources/bx-cli` 的执行位,而它恰好在保护开着时那条常规路径上,
// 于是产品自己给出的修复指引 `upgradeSwitchCommand` 敲下去是 `command not found`。
//
// **准绳是共用的那份判据**(updatepkg.MacOSAppFileMode):这一方哪天不再用它,
// 这条就红 —— 而对面那条 TestStageAppGivesEveryFileTheSharedMode 守着另一方。
// 两条合起来才让「两份清单」在构造上回不来。
func TestWriteMacOSAppTreeUsesTheSharedFileModes(t *testing.T) {
	bundle := filepath.Join(t.TempDir(), "Bx.app")
	app := map[string][]byte{
		"Contents/MacOS/BxMenu":           []byte("menu"),
		"Contents/Info.plist":             []byte("plist"),
		"Contents/Resources/bx-cli":       []byte("cli"),
		"Contents/Resources/bx-bridge":    []byte("bridge"),
		"Contents/Resources/release.json": []byte("{}"),
	}
	if err := writeMacOSAppTree(bundle, app); err != nil {
		t.Fatalf("writeMacOSAppTree: %v", err)
	}
	for name := range app {
		info, err := os.Stat(filepath.Join(bundle, filepath.FromSlash(name)))
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if got, want := info.Mode().Perm(), os.FileMode(updatepkg.MacOSAppFileMode(name)); got != want {
			t.Errorf("直装路径写出的 %s 权限位 = %o, want %o", name, got, want)
		}
	}
}
