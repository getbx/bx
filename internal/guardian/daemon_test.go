package guardian

import (
	"context"
	"errors"
	"testing"

	"github.com/getbx/bx/internal/install"
)

func TestSystemNetworkRestorerPropagatesCancellationToDNSRestore(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	restorer := systemNetworkRestorer{disableDNS: func(got context.Context, service string) (install.DNSStatus, error) {
		called = true
		if service != "" {
			t.Fatalf("service = %q, want auto-detect", service)
		}
		return install.DNSStatus{}, got.Err()
	}}

	err := restorer.Restore(ctx)
	if !called {
		t.Fatal("DNS restore was not called")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Restore error = %v, want context canceled", err)
	}
}

func TestSystemLegacyCoreLifecycleForwardsStopAndRemove(t *testing.T) {
	ctx := context.Background()
	var stopped, removed bool
	lifecycle := systemLegacyCoreLifecycle{
		stop: func(got context.Context) error {
			stopped = got == ctx
			return nil
		},
		remove: func() error {
			removed = true
			return nil
		},
	}
	if err := lifecycle.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.Remove(); err != nil {
		t.Fatal(err)
	}
	if !stopped || !removed {
		t.Fatalf("legacy lifecycle calls = stop:%v remove:%v", stopped, removed)
	}
}
