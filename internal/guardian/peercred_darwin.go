//go:build darwin

package guardian

import (
	"net"

	"golang.org/x/sys/unix"
)

func localPeerCredentials(conn net.Conn) (uint32, bool) {
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, false
	}
	raw, err := unixConn.SyscallConn()
	if err != nil {
		return 0, false
	}
	var uid uint32
	var got bool
	if err := raw.Control(func(fd uintptr) {
		credentials, err := unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if err == nil {
			uid, got = credentials.Uid, true
		}
	}); err != nil {
		return 0, false
	}
	return uid, got
}
