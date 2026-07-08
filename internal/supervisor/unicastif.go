package supervisor

import "math/bits"

// unicastif.go 存放 Windows IP_UNICAST_IF 防环所需的纯字节序逻辑。刻意无 build tag:
// 这是纯算术,可在任意平台(含 Linux 开发机 / CI 三平台)单测,不必等 windows runner。
// 真正调用它的 setsockopt 在 platform_windows.go(//go:build windows)。

// unicastIfV4Value 把接口 index 转成 IPv4 IP_UNICAST_IF 的 setsockopt 取值。
//
// MSDN 规定该选项对 IPv4 须以「网络字节序」(big-endian)存放——像一个前导零的 IP 地址;
// 而 windows.SetsockoptInt 按主机字节序把 int 写进选项缓冲区。bx 的全部 Windows 目标
// (amd64/arm64)均小端,故取值 = index 的字节交换,使其在小端内存里正好排成大端布局。
// 参考 WireGuard conn/bind_windows.go(同款处理)。忘了这步 → 绑错网卡、直连漏回 TUN。
func unicastIfV4Value(index uint32) uint32 {
	return bits.ReverseBytes32(index)
}
