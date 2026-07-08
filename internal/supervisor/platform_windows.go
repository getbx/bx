//go:build windows

// platform_windows.go 是 Windows 平台实现的骨架:先让 GOOS=windows 交叉编译通过 +
// CI 三平台矩阵能在 windows runner 跑平台无关单测。网络层——OpenTUN(wintun,经
// tun/wgbridge.go 的 wireguard tun.Device)、DirectDialer(IP_UNICAST_IF 绑物理网卡防环)、
// Hijack(route / WFP 劫持默认路由 + IPv6 fail-closed)——需真机联调实现,见 CLAUDE.md
// 「跨平台待办 / Windows」。当前 bx run 在 windows 会明确报未实现,不会静默乱改网络。

package supervisor

import (
	"errors"
	"net"

	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

type windowsPlatform struct{}

func newPlatform() platform { return windowsPlatform{} }

var errWindowsUnimplemented = errors.New("bx: Windows 平台网络层尚未实现(TUN/路由劫持待真机联调)")

func (windowsPlatform) OpenTUN(name, addr string, mtu uint32) (stack.LinkEndpoint, tunHandle, func(), error) {
	return nil, tunHandle{}, func() {}, errWindowsUnimplemented
}

// DirectDialer 占位:真实实现需 IP_UNICAST_IF 绑物理网卡(防环,避免绕回 TUN)。
func (windowsPlatform) DirectDialer() *net.Dialer { return &net.Dialer{} }

func (windowsPlatform) Hijack(t tunHandle, serverBypass, userBypass []string) (func(), error) {
	return func() {}, errWindowsUnimplemented
}

func (windowsPlatform) RehijackRoutes(t tunHandle, serverBypass, userBypass []string) error {
	return errWindowsUnimplemented
}
