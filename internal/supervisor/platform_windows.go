//go:build windows

// platform_windows.go 实现 platform 接口的 Windows 部分。第 1/2 步(交叉编译 + CI 三平台矩阵)
// 之后,这里落地第 3 步的前两小步——OpenTUN(wintun,经 tun/wgbridge.go 的 wireguard tun.Device)
// 与 DirectDialer(IP_UNICAST_IF 绑物理网卡防环,Windows 版 SO_MARK)。Hijack(路由劫持 + WFP
// 防 DNS 泄漏 + IPv6 fail-closed)仍待真机联调,见 CLAUDE.md「跨平台待办 / Windows」及
// docs/superpowers/specs/2026-07-08-windows-tun-design.md。
//
// ⚠️ OpenTUN/DirectDialer 依赖 Windows syscall 与 wintun 运行时,无法在 Linux 上 go test,
// 仅交叉编译(GOOS=windows go build)+ 真机验证;纯字节序逻辑抽到 unicastif.go 已单测覆盖。
package supervisor

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"syscall"
	"time"

	"github.com/getbx/bx/internal/tun"
	"golang.org/x/sys/windows"
	wgtun "golang.zx2c4.com/wireguard/tun"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

func newPlatform() platform { return windowsPlatform{} }

type windowsPlatform struct{}

// IP_UNICAST_IF / IPV6_UNICAST_IF 选项号(x/sys/windows 未导出;值同 WinSock 头 = 31,
// 与 WireGuard conn/bind_windows.go 一致)。
const (
	ipUnicastIf   = 31
	ipv6UnicastIf = 31
)

var errWindowsUnimplemented = errors.New("bx: Windows 路由劫持尚未实现(第 3 步待真机联调)")

// OpenTUN 用 wintun 建 Layer-3 适配器(经 wireguard tun.Device),桥接成 gVisor 端点。
// wintun 允许任意适配器名(不像 macOS utun 须 utunN),故 bx 默认的 "bx0" 直接可用。
// 运行时需签名的 wintun.dll 与 bx.exe 同目录(见分发待办)。closeTUN 由 Run 用 defer 接管:
// 停 pump、关设备(wintun 适配器随之移除)。
func (windowsPlatform) OpenTUN(name, addr string, mtu uint32) (stack.LinkEndpoint, tunHandle, func(), error) {
	if name == "" {
		name = "bx0"
	}
	dev, err := wgtun.CreateTUN(name, int(mtu))
	if err != nil {
		return nil, tunHandle{}, nil, fmt.Errorf("创建 wintun 适配器(需管理员 + wintun.dll 同目录): %w", err)
	}
	real, err := dev.Name()
	if err != nil {
		_ = dev.Close()
		return nil, tunHandle{}, nil, fmt.Errorf("取 wintun 适配器名: %w", err)
	}
	link, closeTUN := tun.NewWGEndpoint(dev, mtu)
	return link, tunHandle{Name: real, Addr: addr, MTU: mtu}, closeTUN, nil
}

// DirectDialer 返回把出站绑到物理默认网卡的直连器(bx 自身出站绕过 wintun 防环,
// Windows 版 SO_MARK)。用 GetBestInterfaceEx 查到公网的默认出口 index(仅读路由表、不发包);
// 在 Hijack 装路由之前构造,故拿到的是物理口而非 TUN。v4/v6 各探一次,失败得 0(不绑降级)。
func (windowsPlatform) DirectDialer() *net.Dialer {
	idx4 := bestInterfaceIndex(net.IPv4(1, 1, 1, 1))
	idx6 := bestInterfaceIndex(net.ParseIP("2606:4700:4700::1111"))
	return unicastIfDialer(idx4, idx6)
}

// bestInterfaceIndex 查到该公网目的地的默认出口网卡 index(0=查询失败/无此协议栈)。
// GetBestInterfaceEx 只查路由表、不产生流量,故用固定公网 IP 探测是安全的。
func bestInterfaceIndex(ip net.IP) uint32 {
	if ip == nil {
		return 0
	}
	var sa windows.Sockaddr
	if v4 := ip.To4(); v4 != nil {
		sa = &windows.SockaddrInet4{Addr: [4]byte(v4)}
	} else if v6 := ip.To16(); v6 != nil {
		sa = &windows.SockaddrInet6{Addr: [16]byte(v6)}
	} else {
		return 0
	}
	var idx uint32
	if err := windows.GetBestInterfaceEx(sa, &idx); err != nil {
		return 0
	}
	return idx
}

// unicastIfDialer 用 IP_UNICAST_IF / IPV6_UNICAST_IF 把出站绑到指定网卡 index(0=不绑)。
// 仅对公网目的地绑(shouldBindToDevice):loopback/私网/link-local 不绑——绑物理口反而
// 连不通 127.0.0.1(本机 socks)/内网邻居,交主表原生投递(与 darwin boundIfDialer 同策略)。
// IPv4 的选项值须字节序转换(unicastIfV4Value,见 unicastif.go 头号坑);IPv6 用主机序 index。
func unicastIfDialer(idx4, idx6 uint32) *net.Dialer {
	return &net.Dialer{
		Timeout: 10 * time.Second,
		Control: func(network, address string, c syscall.RawConn) error {
			if !shouldBindToDevice(address) {
				return nil
			}
			v6 := addrIsV6(address)
			if (v6 && idx6 == 0) || (!v6 && idx4 == 0) {
				return nil
			}
			var serr error
			if err := c.Control(func(fd uintptr) {
				h := windows.Handle(fd)
				if v6 {
					serr = windows.SetsockoptInt(h, windows.IPPROTO_IPV6, ipv6UnicastIf, int(idx6))
				} else {
					serr = windows.SetsockoptInt(h, windows.IPPROTO_IP, ipUnicastIf, int(unicastIfV4Value(idx4)))
				}
			}); err != nil {
				return err
			}
			return serr
		},
	}
}

// addrIsV6 判断 "host:port"(或裸 host)的 host 是否为 IPv6 字面量。
func addrIsV6(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		host = address
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	return ip.Unmap().Is6()
}

// Hijack / RehijackRoutes:第 3 步(路由表劫持 + WFP 防 DNS 泄漏 + IPv6 fail-closed)待真机联调。
// 当前返回未实现错误,Run 会在 OpenTUN 成功后于此止步并 defer 干净还原(不静默乱改网络)。
func (windowsPlatform) Hijack(t tunHandle, serverBypass, userBypass []string) (func(), error) {
	return func() {}, errWindowsUnimplemented
}

func (windowsPlatform) RehijackRoutes(t tunHandle, serverBypass, userBypass []string) error {
	return errWindowsUnimplemented
}
