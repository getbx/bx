//go:build unix

package secdir

import (
	"os"
	"path/filepath"
	"testing"
)

// 本文件收纳所有依赖 unix 权限位/属主语义(0o755 精确权限、os.Geteuid、
// 符号链接创建)的用例——在 Windows 上这些前提不成立(目录权限恒报
// 0777、!unix 桩忽略 ownerUID 因 os.Geteuid() 恒为 -1、os.Symlink 需提权),
// 故与跨平台用例(secdir_test.go)分开,只在 unix CI runner 上跑。

func TestEnsureCreatesWithExactMode(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "bx")
	if err := Ensure(path, os.Geteuid(), 0o755); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		t.Fatalf("stat: %v dir=%v", err, info.IsDir())
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("perm = %o want 0755", info.Mode().Perm())
	}
}

func TestEnsureIsIdempotentAndNormalizesPermissions(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "bx")
	if err := os.Mkdir(path, 0o777); err != nil { // 组/其他可写的历史目录
		t.Fatal(err)
	}
	if err := Ensure(path, os.Geteuid(), 0o755); err != nil {
		t.Fatalf("Ensure on existing dir: %v", err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("perm not normalized: %o", info.Mode().Perm())
	}
	if err := Ensure(path, os.Geteuid(), 0o755); err != nil {
		t.Fatalf("second Ensure: %v", err)
	}
}

func TestEnsureSucceedsUnderGroupWritableParent(t *testing.T) {
	// 复刻 macOS /var/run(0775):父目录可被组写,不应妨碍我们创建自有子目录。
	root := t.TempDir()
	parent := filepath.Join(root, "run")
	if err := os.Mkdir(parent, 0o775); err != nil {
		t.Fatal(err)
	}
	if err := Ensure(filepath.Join(parent, "bx"), os.Geteuid(), 0o755); err != nil {
		t.Fatalf("Ensure under group-writable parent: %v", err)
	}
}

func TestEnsureRejectsSymlinkTarget(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "bx")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if err := Ensure(link, os.Geteuid(), 0o755); err == nil {
		t.Fatal("symlink placeholder must be rejected")
	}
}

func TestEnsureRejectsForeignOwner(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "bx")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Ensure(path, os.Geteuid()+4242, 0o755); err == nil {
		t.Fatal("owner mismatch must be rejected")
	}
}
