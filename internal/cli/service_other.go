//go:build !windows

package cli

import "context"

// 非 Windows:bx 由 systemd/launchd 以 Type=simple 前台管理,无需 SCM 握手。
// isWindowsService 恒 false,runAsWindowsService 永不被调用(仅为编译存在)。
func isWindowsService() bool { return false }

func runAsWindowsService(run func(context.Context) error) error {
	return run(context.Background())
}
