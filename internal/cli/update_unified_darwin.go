//go:build darwin

package cli

import (
	"fmt"
	"os/user"
	"strconv"

	"github.com/getbx/bx/internal/install"
)

// directInstallUnifiedUpdate 是 route=="direct" 的落地步骤:把已经在别处校验、展开好
// 的新版 Bx.app(bundlePath)用 install.UnifiedInstall 就地统一安装(App/runtime/
// bridge/Guardian plist/登录项)。ConsoleHome/UID/GID 尽力取(与 appInstallAction 同一
// 套发现逻辑):取不到控制台用户时跳过登录项迁移那一步并提示,不因此让整次更新失败——
// 反正 Off 路径本就没有保护会话要保,这一步纯粹是体验层面的锦上添花。
func directInstallUnifiedUpdate(bundlePath, configPath string) error {
	options := install.UnifiedInstallOptions{
		BundlePath: bundlePath,
		ConfigPath: configPath,
	}
	if uid, err := consoleUserUID(); err != nil {
		fmt.Printf("  (无法定位控制台用户,跳过菜单栏登录项迁移: %v)\n", err)
	} else if account, err := user.LookupId(strconv.Itoa(uid)); err != nil {
		fmt.Printf("  (查找控制台用户失败,跳过菜单栏登录项迁移: %v)\n", err)
	} else if gid, err := strconv.Atoi(account.Gid); err != nil {
		fmt.Printf("  (解析控制台用户组失败,跳过菜单栏登录项迁移: %v)\n", err)
	} else {
		options.ConsoleHome = account.HomeDir
		options.ConsoleUID = uid
		options.ConsoleGID = gid
	}
	_, err := install.UnifiedInstall(options)
	return err
}
