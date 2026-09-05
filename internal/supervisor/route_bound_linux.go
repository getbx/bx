//go:build linux

package supervisor

import (
	"context"
	"errors"
	"os/exec"
	"time"
)

// ErrRouteMissing:问了,没有这条路由。
var ErrRouteMissing = errors.New("route not in table")

// boundRouteArgs 是 `ip route get <dst> oif <dev>` 的实参 —— SO_BINDTODEVICE 的
// socket 与 bx 自己的直连器(shouldBindToDevice)看到的就是这张表。纯函数好测。
func boundRouteArgs(dev, destination string) []string {
	return []string{"route", "get", destination, "oif", dev}
}

// LookupBoundRoute 问「绑在 dev 上的 socket 发往 destination 会走哪」。只读。
func LookupBoundRoute(parent context.Context, dev, destination string) (RouteSelection, error) {
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ip", boundRouteArgs(dev, destination)...).CombinedOutput()
	sel, perr := parseIPRouteGet(string(out), err)
	if errors.Is(perr, ErrNoRouteToDestination) {
		return RouteSelection{}, ErrRouteMissing
	}
	return sel, perr
}

// PhysicalDefaultRoute 复用 metric 感知的默认路由解析(多 WAN 选错 metric 的教训
// 只修在 parseDefaultRoute 一份里)。
func PhysicalDefaultRoute(ctx context.Context) (gateway, device string, err error) {
	return LinuxDefaultRoute(ctx)
}
