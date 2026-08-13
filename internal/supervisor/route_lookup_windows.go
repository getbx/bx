//go:build windows

package supervisor

import (
	"context"
	"errors"
	"net"
	"net/netip"

	"golang.org/x/sys/windows"
)

// RouteSelection 是一次路由查询的结果:内核会把发往该目的地的包交给谁。
type RouteSelection struct {
	Gateway   string
	Interface string
	Reject    bool
}

// ErrNoRouteToDestination:**确知**没有到达该目的地的路由,与「问不出来」分开。
var ErrNoRouteToDestination = errors.New("no route to destination")

// LookupRoute 问内核「发往 destination 的包现在走哪里」。
//
// **全程用 API,不解析任何命令输出。** `route print` / `Get-NetRoute` 的文本格式随
// 版本与语言环境而变(中文 Windows 的表头就不是英文),而这个仓库这一整轮的做法是
// 「判据按真实输出定,不凭记忆」—— 我手上没有 Windows 机器可以取样,那就不写只能
// 靠猜的解析器。GetBestInterfaceEx 返回的是带类型的 index,猜不错。
//
// 它与 DirectDialer 用的是同一个调用(见 platform_windows.go 的 bestInterfaceIndex):
// **只查路由表、不产生流量**,所以拿固定公网地址探测是安全的。
//
// **Reject 恒为 false**,而这是如实的:bx 在 Windows 上的 v6 fail-closed 是把
// `::/1`+`8000::/1` **劫进 TUN**(见 windows_routes.go),不是装 reject 路由 ——
// 这个平台上没有那个形状可认。
func LookupRoute(_ context.Context, destination string, _ bool) (RouteSelection, error) {
	addr, err := netip.ParseAddr(destination)
	if err != nil {
		return RouteSelection{}, err
	}
	var sa windows.Sockaddr
	if addr.Is4() {
		sa = &windows.SockaddrInet4{Addr: addr.As4()}
	} else {
		sa = &windows.SockaddrInet6{Addr: addr.As16()}
	}
	var idx uint32
	if err := windows.GetBestInterfaceEx(sa, &idx); err != nil {
		// **「没有到那里的路由」与「问不出来」必须分开。** Windows 对前者返回
		// ERROR_NETWORK_UNREACHABLE / ERROR_HOST_UNREACHABLE。
		if errors.Is(err, windows.ERROR_NETWORK_UNREACHABLE) ||
			errors.Is(err, windows.ERROR_HOST_UNREACHABLE) {
			return RouteSelection{}, ErrNoRouteToDestination
		}
		return RouteSelection{}, err
	}
	iface, err := net.InterfaceByIndex(int(idx))
	if err != nil {
		// 拿到了 index 却映射不回接口 —— **不冒充成功**。
		return RouteSelection{}, err
	}
	return RouteSelection{Interface: iface.Name}, nil
}
