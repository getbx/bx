package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/guardian"
	urfavecli "github.com/urfave/cli/v2"
)

func TestGuardianCommandIsHiddenWithRequiredFlags(t *testing.T) {
	command := guardianCommandWithDeps(guardianCommandDeps{
		geteuid: func() int { return 0 },
		run:     func(context.Context, guardian.DaemonOptions) error { return nil },
	})
	if !command.Hidden {
		t.Fatal("guardian command must be hidden")
	}
	if !commandHasFlag(command, "config") || !commandHasFlag(command, "listen-dns") {
		t.Fatalf("guardian flags = %+v", command.Flags)
	}
}

func TestGuardianCommandRefusesNonRootWithoutRunningDaemon(t *testing.T) {
	runs := 0
	app := New()
	app.Commands = []*urfavecli.Command{guardianCommandWithDeps(guardianCommandDeps{
		geteuid: func() int { return 501 },
		run: func(context.Context, guardian.DaemonOptions) error {
			runs++
			return nil
		},
	})}
	err := app.Run([]string{"bx", "guardian"})
	if err == nil || !strings.Contains(err.Error(), "root") {
		t.Fatalf("error = %v, want root refusal", err)
	}
	if runs != 0 {
		t.Fatalf("daemon ran %d times", runs)
	}
}

func TestGuardianCommandPassesConfigurationToInjectedDaemon(t *testing.T) {
	wantErr := errors.New("stop test daemon")
	var got guardian.DaemonOptions
	app := New()
	app.Commands = []*urfavecli.Command{guardianCommandWithDeps(guardianCommandDeps{
		geteuid: func() int { return 0 },
		run: func(_ context.Context, options guardian.DaemonOptions) error {
			got = options
			return wantErr
		},
	})}
	err := app.Run([]string{"bx", "guardian", "--config", "/tmp/config.yaml", "--listen-dns", "127.0.0.1:1053"})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
	if got.ConfigPath != "/tmp/config.yaml" || got.DNSListen != "127.0.0.1:1053" {
		t.Fatalf("options = %+v", got)
	}
}
