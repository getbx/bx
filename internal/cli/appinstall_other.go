//go:build !darwin

package cli

import (
	"errors"

	urfavecli "github.com/urfave/cli/v2"
)

func appInstallAction(c *urfavecli.Context) error {
	return errors.New("app-install is only supported on macOS")
}
