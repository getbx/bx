//go:build !windows

package provision

import "golang.org/x/sys/unix"

// statfsFree 用 statfs 取 dir 所在文件系统对非特权用户可用的块数(Bavail,
// 不是 Bfree —— root 预留的那一块不算可用,bx 以 root 跑但没理由把预留吃掉)。
func statfsFree(dir string) (uint64, bool) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0, false
	}
	return uint64(st.Bavail) * uint64(st.Bsize), true
}
