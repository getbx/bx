//go:build unix

package runtimedir

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/getbx/bx/internal/release"
)

// TestInstallVersionChmodsIgnoringUmask 证明 InstallVersion 不依赖调用方 umask:
// 即便进程 umask 收紧到 0o077(sudo 环境常见配置),落地的 runtime 目录/文件权限
// 仍要保持对所有用户可读可执行,否则非 root 的 bridge(/usr/local/bin/bx)与
// 菜单栏进程读不到 runtime。umask 是 unix 概念,windows 无对应语义,故本测试
// 仅在 unix(linux/darwin)构建。
func TestInstallVersionChmodsIgnoringUmask(t *testing.T) {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)

	root := filepath.Join(t.TempDir(), "runtime")
	dir, err := InstallVersion(root, payloadFor(t, "1.0.0", []byte("cli-v1")))
	if err != nil {
		t.Fatalf("InstallVersion: %v", err)
	}

	rootFi, err := os.Stat(root)
	if err != nil || rootFi.Mode().Perm() != 0o755 {
		t.Fatalf("root perm = %v err=%v", rootFi.Mode(), err)
	}
	dirFi, err := os.Stat(dir)
	if err != nil || dirFi.Mode().Perm() != 0o755 {
		t.Fatalf("version dir perm = %v err=%v", dirFi.Mode(), err)
	}
	bxFi, err := os.Stat(filepath.Join(dir, "bx"))
	if err != nil || bxFi.Mode().Perm() != 0o755 {
		t.Fatalf("bx perm = %v err=%v", bxFi.Mode(), err)
	}
	releaseFi, err := os.Stat(filepath.Join(dir, release.FileName))
	if err != nil || releaseFi.Mode().Perm() != 0o644 {
		t.Fatalf("release.json perm = %v err=%v", releaseFi.Mode(), err)
	}
}
