package bridge

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type fakeInfo struct {
	mode os.FileMode
	dir  bool
}

func (f fakeInfo) Name() string       { return "x" }
func (f fakeInfo) Size() int64        { return 1 }
func (f fakeInfo) Mode() os.FileMode  { return f.mode }
func (f fakeInfo) ModTime() time.Time { return time.Time{} }
func (f fakeInfo) IsDir() bool        { return f.dir }
func (f fakeInfo) Sys() any           { return nil }

type fixture struct {
	resolved string
	uid      map[string]int
	mode     map[string]os.FileMode
	dirs     map[string]bool
}

func (f fixture) sys() Sys {
	return Sys{
		Stat: func(path string) (os.FileInfo, error) {
			mode, ok := f.mode[path]
			if !ok {
				return nil, fs.ErrNotExist
			}
			return fakeInfo{mode: mode, dir: f.dirs[path]}, nil
		},
		EvalSymlinks: func(path string) (string, error) {
			if f.resolved == "" {
				return "", errors.New("dangling")
			}
			return f.resolved, nil
		},
		OwnerUID: func(info os.FileInfo) (int, bool) {
			// 测试中按 mode 独一无二地反查路径不可行;用统一 uid 表:
			// fixture 用同一个 uid 值覆盖所有路径即可满足用例。
			for _, uid := range f.uid {
				return uid, true
			}
			return 0, false
		},
	}
}

const root = "/Library/Application Support/bx/runtime"

func healthy() fixture {
	versionDir := filepath.Join(root, "1.0.0")
	return fixture{
		resolved: versionDir,
		uid:      map[string]int{versionDir: 0},
		mode: map[string]os.FileMode{
			versionDir:                      0o755 | os.ModeDir,
			filepath.Join(versionDir, "bx"): 0o755,
		},
		dirs: map[string]bool{versionDir: true},
	}
}

func TestResolveHealthy(t *testing.T) {
	exe, err := ResolveExecutable(root, healthy().sys())
	if err != nil {
		t.Fatalf("ResolveExecutable: %v", err)
	}
	want := filepath.Join(root, "1.0.0", "bx")
	if exe != want {
		t.Fatalf("exe = %q want %q", exe, want)
	}
}

func TestResolveRejectsEscape(t *testing.T) {
	f := healthy()
	f.resolved = "/private/tmp/evil"
	if _, err := ResolveExecutable(root, f.sys()); err == nil {
		t.Fatal("want escape rejected")
	}
}

func TestResolveRejectsNonRootOwner(t *testing.T) {
	f := healthy()
	f.uid = map[string]int{filepath.Join(root, "1.0.0"): 501}
	if _, err := ResolveExecutable(root, f.sys()); err == nil {
		t.Fatal("want non-root owner rejected")
	}
}

func TestResolveRejectsWritableMode(t *testing.T) {
	f := healthy()
	f.mode[filepath.Join(root, "1.0.0", "bx")] = 0o775 // group 可写
	if _, err := ResolveExecutable(root, f.sys()); err == nil {
		t.Fatal("want group-writable rejected")
	}
}

func TestResolveRejectsMissingRuntime(t *testing.T) {
	f := fixture{}
	if _, err := ResolveExecutable(root, f.sys()); err == nil {
		t.Fatal("want missing runtime error")
	}
}

func TestResolveRejectsNonExecutable(t *testing.T) {
	f := healthy()
	f.mode[filepath.Join(root, "1.0.0", "bx")] = 0o644
	if _, err := ResolveExecutable(root, f.sys()); err == nil {
		t.Fatal("want non-executable rejected")
	}
}
