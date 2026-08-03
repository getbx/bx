//go:build !darwin

package cli

import "fmt"

// directInstallUnifiedUpdate 的非 darwin 兜底:统一布局(Bx.app)整套目前只在 macOS 上
// 存在,unifiedLayoutActive() 在其余 GOOS 恒 false,updateUnifiedMacOS 因此永远不会在
// 这些平台上被调用到这一步——这个 stub 纯粹是为了让 update.go(无 build tag)在所有
// GOOS 下都能编译,install.UnifiedInstall 等类型仅在 darwin 构建下存在。
func directInstallUnifiedUpdate(bundlePath, configPath string) error {
	return fmt.Errorf("统一布局在线更新仅支持 macOS(bundlePath=%s, configPath=%s)", bundlePath, configPath)
}
