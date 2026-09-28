//go:build !darwin && !linux

package supervisor

import (
	"context"
	"net/netip"

	"github.com/getbx/bx/internal/stats"
)

func collectNetworkWarnings(context.Context, func() []netip.Prefix, *strayTracker) []stats.Warning {
	return nil
}
