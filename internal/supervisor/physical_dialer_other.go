//go:build !darwin

package supervisor

import (
	"context"
	"errors"
	"net"
)

// PhysicalInterfaceDialer 只在 darwin 上有(IP_BOUND_IF 不需要 root)。linux 上绑网卡
// 要 CAP_NET_RAW,而 `bx leakcheck` 拒绝 root;windows 没做过。**如实说不支持**,
// 不退回一个不绑网卡的拨号器 —— 那会让两条「路」走同一条路,而报告仍把它们当成
// 两份独立观测并排出示。
func PhysicalInterfaceDialer(context.Context) (*net.Dialer, error) {
	return nil, errors.New("probing directly from the physical network interface is only available on macOS")
}
