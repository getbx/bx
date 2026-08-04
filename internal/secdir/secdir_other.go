//go:build !unix

package secdir

import (
	"fmt"
	"os"
)

// Ensure 确保 path 存在且是目录,权限归一化为 mode。NTFS 无 POSIX uid 语义,
// ownerUID 在非 unix 平台被忽略——签名与 unix 实现保持一致,便于调用方统一
// 编码;真正的访问控制在 unix 上靠属主+权限位,在 windows 上留给 ACL(不在
// 本包职责范围)。
func Ensure(path string, ownerUID int, mode os.FileMode) error {
	_ = ownerUID
	if err := validatePath(path); err != nil {
		return err
	}
	if err := ValidateMode(mode); err != nil {
		return err
	}
	if err := os.MkdirAll(path, mode); err != nil {
		return err
	}
	info, err := os.Stat(path)
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
