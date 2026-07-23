//go:build windows

package cli

import (
	"fmt"

	"github.com/urfave/cli/v2"
)

// rootAction 是无子命令时的行为:windows 下双击/无参启动托盘(回到图标);
// 有未知参数则出帮助并报错。
func rootAction(c *cli.Context) error {
	if c.Args().Len() == 0 {
		return trayAction(c)
	}
	_ = cli.ShowAppHelp(c)
	return fmt.Errorf("未知命令: %s", c.Args().First())
}
