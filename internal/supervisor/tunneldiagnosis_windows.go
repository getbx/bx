//go:build windows

package supervisor

import "golang.org/x/sys/windows"

// platformDialFailuresBeforeTheSYNLeaves:posixDialFailuresBeforeTheSYNLeaves 那四个
// 在 Windows 上的**真实**对应值。
//
// 理由见 dialFailuresBeforeTheSYNLeaves 头上那段:`syscall.ENETUNREACH` 一族在
// Windows 上是 `APPLICATION_ERROR + iota` 造出来的,winsock 永远不返回它们,而
// `syscall.Errno.Is` 也不跨映射 —— 少了这张表,Windows 上每一次「本机不可达」
// 都会被报成 tunnel_unreachable(「那台服务器没有应答」),而 SYN 一个都没出去。
//
// 加一行 posix 值时这里也要加它的 WSAE 孪生:由
// TestEveryLocalDialFailureHasAWinsockTwin 钉住(它读源码 —— Windows 的单测
// 在 CI 的 windows runner 上才跑,而 verify.sh 只交叉**编译** Windows)。
var platformDialFailuresBeforeTheSYNLeaves = []error{
	windows.WSAENETUNREACH,
	windows.WSAEHOSTUNREACH,
	windows.WSAEACCES,
	windows.WSAEADDRNOTAVAIL,
}
