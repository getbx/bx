//go:build !darwin && !linux

package guardian

import "net"

func localPeerCredentials(net.Conn) (uint32, bool) { return 0, false }
