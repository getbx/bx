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
	if _, err := install.UnifiedInstall(options); err != nil {
		return err
	}
	// 安装刚改写过菜单栏 plist(runtime 可执行路径随版本走),必须强制重载才能让
	// 新版本的菜单栏生效。此前是靠 bx up 无条件 bootout+bootstrap 顺带做掉的,
	// 而那条路径已经改成「已加载就不重载」(否则每次启动保护都闪一下图标),
	// 所以这里必须显式重载,不能再指望它。
	// 重载失败不让更新失败:菜单栏是 UI 壳,下次登录也会带上新 plist。
	if options.ConsoleUID > 0 {
		if err := ensureMacOSMenuReloaded(options.ConsoleUID, true); err != nil {
			fmt.Printf("  (菜单栏未能重载,下次登录后生效: %v)\n", err)
		}
	}
	return nil
}
