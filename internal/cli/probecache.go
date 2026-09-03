//go:build unix

package cli

import (
	"os"
	"syscall"
)

// statDirOwner 取一个目录的属主 uid。**拿不到就说拿不到**(ok=false)——
// 目录不存在、平台不给 Stat_t,都不该被读成「属主是 0」。
func statDirOwner(dir string) (int, bool) {
	info, err := os.Stat(dir)
	if err != nil {
		return 0, false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(st.Uid), true
}
