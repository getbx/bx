//go:build linux

package guardian

import (
	"net"

	"golang.org/x/sys/unix"
)

// SO_PEERCRED 版的对端凭据:与 darwin 的 LOCAL_PEERCRED 同构。取不到一律
// (0, false)——authorizeOwnerPeer 对 got=false 是拒绝,fail-closed 不变。
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
		credentials, err := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if err == nil {
			uid, got = credentials.Uid, true
		}
	}); err != nil {
		return 0, false
	}
	return uid, got
}
