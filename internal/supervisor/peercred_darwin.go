//go:build darwin

package supervisor

import (
	"net"

	"golang.org/x/sys/unix"
)

const peerCredSupported = true

func peerCredUID(conn net.Conn) (uint32, bool) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, false
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, false
	}
	var uid uint32
	var got bool
	if err := raw.Control(func(fd uintptr) {
		cred, err := unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if err == nil {
			uid, got = cred.Uid, true
		}
	}); err != nil {
		return 0, false
	}
	return uid, got
}
