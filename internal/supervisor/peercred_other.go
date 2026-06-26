//go:build !linux

package supervisor

import "net"

// peerCredSupported=false:本平台暂不取 peer-cred(darwin 待实现 LOCAL_PEERCRED)。
const peerCredSupported = false

// peerCredUID 在非 Linux 平台暂不取 peer-cred(gotUID=false → fail-closed 拒绝改动类)。
// darwin 真机应实现 LOCAL_PEERCRED(getsockopt + xucred)后收紧。
func peerCredUID(conn net.Conn) (uint32, bool) { return 0, false }
