//go:build windows

package cli

import (
	"fmt"

	"github.com/getbx/bx/internal/install"
	"github.com/urfave/cli/v2"
)

func autostartAction(c *cli.Context) error {
	want, status, err := parseAutostartArg(c.Args().First())
	if err != nil {
		return err
	}
	if status {
		if install.AutostartEnabled() {
			fmt.Println("开机自启:开")
		} else {
			fmt.Println("开机自启:关")
		}
		return nil
	}
	if err := install.SetAutostart(*want); err != nil {
		return err
	}
	if *want {
		fmt.Println("✅ 已设为开机自启(服务 + 托盘图标)。")
	} else {
		fmt.Println("✅ 已取消开机自启。")
	}
	return nil
}
