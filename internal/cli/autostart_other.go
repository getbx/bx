//go:build !windows

package cli

import (
	"errors"

	"github.com/urfave/cli/v2"
)

func autostartAction(_ *cli.Context) error {
	return errors.New("bx autostart 目前仅支持 Windows")
}
