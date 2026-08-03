//go:build !windows

package bridge

import (
	"os"
	"path/filepath"
	"syscall"
)

func RealSys() Sys {
	return Sys{
		Stat:         os.Stat,
		EvalSymlinks: filepath.EvalSymlinks,
		OwnerUID: func(info os.FileInfo) (int, bool) {
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok {
				return 0, false
			}
			return int(stat.Uid), true
		},
	}
}
