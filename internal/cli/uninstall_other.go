//go:build !darwin

package cli

import (
	"fmt"

	urfavecli "github.com/urfave/cli/v2"
)

// uninstallDarwinAction 在非 darwin 平台不可达(uninstallAction 按 runtime.GOOS
// 分派),此处仅为满足跨平台编译。
func uninstallDarwinAction(*urfavecli.Context) error {
	return fmt.Errorf("darwin uninstall is unsupported on this platform")
}
