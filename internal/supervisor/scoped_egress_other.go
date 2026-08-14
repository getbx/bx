//go:build !darwin

package supervisor

import "context"

// DirectEgressReachable 在非 darwin 上恒报「问不出来」。
//
// **不是「出不去」**:`IP_BOUND_IF` 那套 scoped 路由语义是 macOS 特有的,
// Linux 用 SO_MARK、Windows 用 IP_UNICAST_IF,都不经 scoped 路由表 ——
// 那里这个问题根本不成立,由 observe.NotApplicableForPlatform 声明。
func DirectEgressReachable(context.Context) (reachable bool, known bool, err error) {
	return false, false, nil
}
