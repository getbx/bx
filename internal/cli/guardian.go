package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/getbx/bx/internal/guardian"
	urfavecli "github.com/urfave/cli/v2"
)

type guardianCommandDeps struct {
	geteuid func() int
	run     func(context.Context, guardian.DaemonOptions) error
}

func guardianCommand() *urfavecli.Command {
	return guardianCommandWithDeps(guardianCommandDeps{geteuid: os.Geteuid, run: guardian.RunDaemon})
}

func guardianCommandWithDeps(deps guardianCommandDeps) *urfavecli.Command {
	return &urfavecli.Command{
		Name:   "guardian",
		Usage:  "run the macOS Guardian lifecycle daemon",
		Hidden: true,
		Flags: []urfavecli.Flag{
			&urfavecli.StringFlag{Name: "config", Value: defaultConfigPath},
			&urfavecli.StringFlag{Name: "listen-dns", Value: darwinDNSListen},
		},
		Action: func(c *urfavecli.Context) error {
			if deps.geteuid == nil || deps.geteuid() != 0 {
				return fmt.Errorf("bx guardian requires root")
			}
			if deps.run == nil {
				return fmt.Errorf("Guardian daemon runner unavailable")
			}
			ctx, cancel := signal.NotifyContext(c.Context, syscall.SIGINT, syscall.SIGTERM)
			defer cancel()
			return deps.run(ctx, guardian.DaemonOptions{ConfigPath: c.String("config"), DNSListen: c.String("listen-dns"), SocketPath: guardian.SocketPath})
		},
	}
}
