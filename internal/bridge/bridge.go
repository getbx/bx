// Package bridge 实现 /usr/local/bin/bx 稳定入口对 root runtime 的解析与安全校验。
package bridge

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const RepairHint = "bx runtime is missing or damaged. Open Bx.app to install or repair bx.\n(bx 运行时缺失或损坏:请打开 Bx.app 完成安装或修复。)"

type Sys struct {
	Stat         func(string) (os.FileInfo, error)
	EvalSymlinks func(string) (string, error)
	OwnerUID     func(os.FileInfo) (int, bool)
}

func ResolveExecutable(root string, sys Sys) (string, error) {
	resolvedRoot, err := sys.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("runtime root missing: %w", err)
	}
	versionDir, err := sys.EvalSymlinks(filepath.Join(root, "current"))
	if err != nil {
		return "", fmt.Errorf("runtime current missing: %w", err)
	}
	if !strings.HasPrefix(versionDir+string(filepath.Separator), resolvedRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("runtime current escapes %s", root)
	}
	if err := requireRootOwned(sys, versionDir, true, false); err != nil {
		return "", err
	}
	executable := filepath.Join(versionDir, "bx")
	if err := requireRootOwned(sys, executable, false, true); err != nil {
		return "", err
	}
	return executable, nil
}

func requireRootOwned(sys Sys, path string, wantDir, wantExecutable bool) error {
	info, err := sys.Stat(path)
	if err != nil {
		return fmt.Errorf("runtime path %s missing: %w", path, err)
	}
	if info.IsDir() != wantDir {
		return fmt.Errorf("runtime path %s wrong type", path)
	}
	if !wantDir && !info.Mode().IsRegular() {
		return fmt.Errorf("runtime path %s not a regular file", path)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("runtime path %s is group/other writable", path)
	}
	if wantExecutable && info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("runtime path %s not executable", path)
	}
	uid, ok := sys.OwnerUID(info)
	if !ok || uid != 0 {
		return fmt.Errorf("runtime path %s not owned by root", path)
	}
	return nil
}
