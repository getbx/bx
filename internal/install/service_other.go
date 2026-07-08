//go:build !windows

package install

// 非 Windows 平台的 Windows 服务操作桩:仅为让 install.go 的 `case "windows"` 分支在所有平台
// 编译通过。运行期由 runtime.GOOS=="windows" 守卫,这些桩在 linux/darwin 上永不被调用。
import "errors"

var errNotWindows = errors.New("bx: Windows 服务操作仅在 Windows 可用")

func windowsInstallService(execStart string) error { return errNotWindows }
func windowsEnableService() error                  { return errNotWindows }
func windowsDisableService() error                 { return errNotWindows }
func windowsRestartService() error                 { return errNotWindows }
func windowsUninstallService() error               { return errNotWindows }
func windowsServiceInstalled() bool                { return false }
func windowsServiceExecCmd() (string, error)       { return "", errNotWindows }
func windowsServiceState(action string) string     { return "unknown" }
