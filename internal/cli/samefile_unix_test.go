//go:build !windows

package cli

import "os"

// sameFile 判断两次 Stat 拿到的是不是同一个 inode。用它来区分「就地截断改写」
// 与「临时文件 + rename」——后者必然换掉目录项指向的 inode。
func sameFile(a, b os.FileInfo) bool { return os.SameFile(a, b) }
