package update

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// Bx.app 里每一个**必需**文件都要有一个想过的权限位,而那三个可执行的必须是 0755。
//
// 穷举 requiredMacOSAppFiles 而不是手抄一份清单:往那份清单里加一个文件时这条会
// 逼着人回答「它要不要执行位」,而不是让它默默落进 0644。
func TestEveryRequiredAppFileHasADeliberateMode(t *testing.T) {
	wantExecutable := map[string]bool{
		"Contents/MacOS/BxMenu":        true,
		"Contents/Resources/bx-cli":    true,
		"Contents/Resources/bx-bridge": true,
	}
	for _, name := range requiredMacOSAppFiles {
		got := MacOSAppFileMode(name)
		want := fs.FileMode(0o644)
		if wantExecutable[name] {
			want = 0o755
		}
		if got != want {
			t.Errorf("%s 的权限位 = %o, want %o", name, got, want)
		}
		delete(wantExecutable, name)
	}
	for name := range wantExecutable {
		t.Errorf("%s 被当成可执行文件,但它已经不在 requiredMacOSAppFiles 里了 —— 陈旧条目什么也不守", name)
	}
}

// Guardian 主持的那条升级路径(stageApp)必须用同一份判据。
//
// **这条打在盘上真实的权限位上**,不是打在「它调用了那个函数」上:后者挡不住
// 「调了,然后又被下面一行 Chmod 覆盖掉」,而 stageApp 里恰好两样都有。
func TestStageAppGivesEveryFileTheSharedMode(t *testing.T) {
	root := t.TempDir()
	ops := &installTestFileOps{root: root}
	dest := filepath.Join(root, "Bx.app")
	payload := MacOSPayload{Menu: map[string][]byte{}}
	for _, name := range requiredMacOSAppFiles {
		payload.Menu[name] = []byte("payload for " + name)
	}

	if err := stageApp(ops, dest, payload, os.Getuid(), os.Getgid()); err != nil {
		t.Fatalf("stageApp: %v", err)
	}
	for _, name := range requiredMacOSAppFiles {
		info, err := os.Stat(filepath.Join(dest, filepath.FromSlash(name)))
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if got, want := info.Mode().Perm(), MacOSAppFileMode(name); got != want {
			t.Errorf("升级路径写出的 %s 权限位 = %o, want %o\n"+
				"  真机后果:bx-cli 少了执行位,而 upgradeSwitchCommand 正是直接执行它",
				name, got, want)
		}
	}
}
