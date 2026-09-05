//go:build darwin

package supervisor

import (
	"context"
	"errors"
	"strings"
)

// ErrRouteMissing:问了,表里没有这条(scoped 表 `not in table`)。它是**答案**,
// 不是查询失败 —— 绑在物理网卡上的 socket 连这个地址会 ENETUNREACH。
var ErrRouteMissing = errors.New("route not in table")

// LookupBoundRoute 问「绑在 dev 上的 socket 发往 destination 会走哪」。
//
// macOS 的 IP_BOUND_IF 只查该接口的 scoped 路由表;Tailscale、部分 VPN、bx 自己
// 的直连器都是这种 socket。它与普通 socket 看到的路由**可以不同**,而把两者混为
// 一谈正是 2026-09-04「bx 吞了 Tailscale」那次误判的机制。只读,不发包。
func LookupBoundRoute(ctx context.Context, dev, destination string) (RouteSelection, error) {
	iface, err := lookupScopedRouteDarwin(ctx, dev, destination)
	if err != nil {
		if isScopedRouteMissing(err) {
			return RouteSelection{}, ErrRouteMissing
		}
		return RouteSelection{}, err
	}
	return RouteSelection{Interface: strings.TrimSpace(iface)}, nil
}

// PhysicalDefaultRoute 是默认路由所在的物理网关与网卡(split-default 下 `default`
// 仍指物理网关,故这条查询在 bx 开着时照样成立)。
func PhysicalDefaultRoute(ctx context.Context) (gateway, device string, err error) {
	gw, dev, err := defaultRouteDarwinContext(ctx)
	return strings.TrimSpace(gw), strings.TrimSpace(dev), err
}
