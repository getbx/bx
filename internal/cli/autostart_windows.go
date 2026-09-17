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
			fmt.Println("Start at boot: on")
		} else {
			fmt.Println("Start at boot: off")
		}
		return nil
	}
	if err := install.SetAutostart(*want); err != nil {
		return err
	}
	if *want {
		fmt.Println("✅ Start-at-boot is on (the service and the tray icon).")
	} else {
		fmt.Println("✅ Start-at-boot is off.")
	}
	return nil
}
