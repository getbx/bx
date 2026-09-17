//go:build !windows

package cli

import (
	"errors"

	"github.com/urfave/cli/v2"
)

func autostartAction(_ *cli.Context) error {
	return errors.New("bx autostart is only supported on Windows for now")
}
