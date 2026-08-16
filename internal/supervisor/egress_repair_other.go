//go:build !darwin

package supervisor

import (
	"context"
	"errors"
)

// liveEgressRepair 在非 darwin 上不做任何事。
//
// 这条 scoped 路由是 macOS 特有的:Linux 的 DirectDialer 用 SO_MARK + 策略路由,
// 不依赖 per-interface scoped 表。**返回错误而不是 nil**:nil 会让循环打出一行
// 「已重装」而它什么都没做 —— 不过循环在这些平台上根本不会启动(见 run.go)。
func liveEgressRepair(context.Context) error {
	return errors.New("scoped 默认路由修复只在 darwin 上适用")
}
