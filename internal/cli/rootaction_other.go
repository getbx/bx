//go:build !windows

package cli

import (
	"fmt"

	"github.com/urfave/cli/v2"
)

// rootAction 在非 windows 保持默认:无参出帮助,有未知参数出帮助并报错。
func rootAction(c *cli.Context) error {
	_ = cli.ShowAppHelp(c)
	if c.Args().Len() > 0 {
		return fmt.Errorf("未知命令: %s", c.Args().First())
	}
	return nil
}
