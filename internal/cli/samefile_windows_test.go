//go:build windows

package cli

import "os"

// Windows 上 os.SameFile 依赖 volume/index 信息,对已被 rename 替换的路径同样
// 可靠;保持与 unix 一致的实现即可(此包的 macOS 专属逻辑本就不在 Windows 跑)。
func sameFile(a, b os.FileInfo) bool { return os.SameFile(a, b) }
