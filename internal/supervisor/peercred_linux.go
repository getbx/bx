//go:build linux

package supervisor

import (
	"net"

	"golang.org/x/sys/unix"
)

// peerCredUID 经 SO_PEERCRED 取 unix 连接对端进程的 uid。
func peerCredUID(conn net.Conn) (uint32, bool) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, false
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, false
	}
	var cred *unix.Ucred
	var serr error
	if err := raw.Control(func(fd uintptr) {
		cred, serr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil || serr != nil || cred == nil {
		return 0, false
	}
	return cred.Uid, true
}
