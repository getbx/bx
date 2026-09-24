//go:build darwin

package supervisor

import (
	"context"
	"fmt"
	"net"
)

// PhysicalInterfaceDialer 返回绑在物理默认网卡上的拨号器(IP_BOUND_IF),给**不属于
// Core** 的只读工具用 —— 今天唯一的消费方是 `bx leakcheck --compare-direct` 的
// 「绕过隧道」那条路。与 DirectDialer 同一个 boundIfDialer,网卡取自
// PhysicalDefaultRoute(split-default 下 `default` 仍指物理网关,bx 开着时照样成立)。
//
// **它只查该接口的 scoped 路由表**:bx 开着时 Hijack 装了那条 scoped 默认路由;
// 别的 VPN 在跑、而这是一台单网络服务的 Mac 时,那张表可能是空的,拨号会在本机
// ENETUNREACH —— 调用方必须把那一类判成「没测成」,不是「连不上」。
func PhysicalInterfaceDialer(ctx context.Context) (*net.Dialer, error) {
	_, dev, err := PhysicalDefaultRoute(ctx)
	if err != nil {
		return nil, fmt.Errorf("finding the physical network interface: %w", err)
	}
	ifi, err := net.InterfaceByName(dev)
	if err != nil {
		return nil, fmt.Errorf("looking up the physical network interface %s: %w", dev, err)
	}
	return boundIfDialer(ifi.Index), nil
}
