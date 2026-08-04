// Package secdir 确保一个由本进程属主拥有、他人不可写的目录存在——用于存放
// unix socket 等「路径本身即信任边界」的运行期对象。
// darwin/unix 实现全程 fd 锚定(openat + O_NOFOLLOW),父链上的符号链接一律拒绝,
// 因此即便父目录(如 macOS 的 /var/run,出厂 0775 root:daemon)可被组写,
// 也无法通过预置符号链接把我们的目录劫持到别处。
package secdir

import (
	"fmt"
	"os"
	"path/filepath"
)

// disallowedModeBits 是 mode 中不允许出现的位:group/other 写位。
const disallowedModeBits = 0o022

// ValidateMode 是纯逻辑校验(供调用方与测试复用):mode 不得含 0o022
// (group-writable / other-writable)。
func ValidateMode(mode os.FileMode) error {
	if mode.Perm()&disallowedModeBits != 0 {
		return fmt.Errorf("secdir: mode %o must not be group/other writable", mode.Perm())
	}
	return nil
}

// validatePath 校验 path 必须是绝对路径且已是 filepath.Clean 后的形式
// (不含 "." "/.." "//" 等)。两平台实现共用。
func validatePath(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fmt.Errorf("secdir: path must be clean and absolute: %q", path)
	}
	return nil
}
