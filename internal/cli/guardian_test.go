package cli

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/getbx/bx/internal/guardian"
	"github.com/getbx/bx/internal/install"
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

func TestInstallGuardianSocketPathMatchesGuardian(t *testing.T) {
	if install.GuardianSocketPath != guardian.SocketPath {
		t.Fatalf("install.GuardianSocketPath = %q, guardian.SocketPath = %q — 两处必须一致(install 不能 import guardian:会成环)", install.GuardianSocketPath, guardian.SocketPath)
	}
}

// TestDefaultMacOSLifecycleDepsMutationClientOutlivesServerBudget guards
// against guardian.NewClient's default 30s HTTP timeout firing on a Guardian
// mutation (Up/Down/Migrate) queued behind a startup Recover: the server-side
// mutation budget is guardianMutationTimeout (60s in
// internal/guardian/localapi.go), so a client that gives up sooner can see
// "Client.Timeout exceeded while awaiting headers" for a request the daemon
// was actually about to satisfy. defaultMacOSLifecycleDeps must use
// guardian.NewClientWithTimeout with real margin over that 60s budget.
func TestDefaultMacOSLifecycleDepsMutationClientOutlivesServerBudget(t *testing.T) {
	deps := defaultMacOSLifecycleDeps()
	client, ok := deps.client.(*guardian.Client)
	if !ok {
		t.Fatalf("client type = %T, want *guardian.Client", deps.client)
	}
	if client.HTTPClient == nil {
		t.Fatal("HTTPClient is nil: falls back to guardian's default 30s timeout")
	}
	const serverMutationBudget = time.Minute
	if client.HTTPClient.Timeout <= serverMutationBudget {
		t.Fatalf("mutation client timeout = %v, want > %v (the server-side mutation budget)", client.HTTPClient.Timeout, serverMutationBudget)
	}
	if client.HTTPClient.Timeout != guardianMutationClientTimeout {
		t.Fatalf("mutation client timeout = %v, want %v", client.HTTPClient.Timeout, guardianMutationClientTimeout)
	}
}
