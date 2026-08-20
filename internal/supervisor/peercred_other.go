//go:build !linux && !darwin

package supervisor

import "net"

// peerCredSupported=false:本平台(既非 linux 也非 darwin,见 build tag)不取
// peer-cred。**linux 与 darwin 都已实现**(SO_PEERCRED / LOCAL_PEERCRED),
// 别再照旧注释以为 macOS 还是这条路 —— 那句话在 peercred_darwin.go 落地之后
// 就过时了,而它恰好是「控制面上没有真正的门」这个错误前提的来源。
const peerCredSupported = false

// peerCredUID 在这些平台恒 (0,false) ⇒ authorizeMutation 恒 false ⇒ fail-closed:
// 改动类路由与带副作用的 /v0/apps 一律拒绝。谁要移植,先实现这个函数。
func peerCredUID(conn net.Conn) (uint32, bool) { return 0, false }
