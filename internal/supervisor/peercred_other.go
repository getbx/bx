//go:build !linux

package supervisor

import "net"

// peerCredUID 在非 Linux 平台暂不取 peer-cred(known=false → 开发态宽松)。
// darwin 真机应实现 LOCAL_PEERCRED(getsockopt + xucred)后收紧。
func peerCredUID(conn net.Conn) (uint32, bool) { return 0, false }
