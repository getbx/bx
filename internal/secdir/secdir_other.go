//go:build !unix

package secdir

import (
	"fmt"
	"os"
)

// Ensure 确保 path 存在且是目录,权限归一化为 mode。NTFS 无 POSIX uid 语义,
// ownerUID 在非 unix 平台被忽略——签名与 unix 实现保持一致,便于调用方统一
// 编码。
//
// 注意:本包的安全契约仅在 unix 上有效。在 windows 上,os.Chmod 仅切换
// 只读位,无法提供属主或组/其他写位的保证;访问控制请依赖 ACL 等系统机制。
func Ensure(path string, ownerUID int, mode os.FileMode) error {
	_ = ownerUID
	if err := validatePath(path); err != nil {
		return err
	}
	if err := ValidateMode(mode); err != nil {
		return err
	}

	// 在 MkdirAll 前检查:若 path 已存在且是符号链接或非目录,直接拒绝。
	// 这确保符号链接占位在所有平台上都被拦截。
	info, err := os.Lstat(path)
	if err == nil {
		// path 存在
		if info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeIrregular != 0 {
			return fmt.Errorf("secdir: %q is a symlink or irregular file", path)
		}
		if !info.IsDir() {
			return fmt.Errorf("secdir: %q exists and is not a directory", path)
		}
	}

	if err := os.MkdirAll(path, mode); err != nil {
		return err
	}
	info, err = os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("secdir: %q exists and is not a directory", path)
	}
	if err := os.Chmod(path, mode); err != nil {
		return err
	}
	return nil
}
