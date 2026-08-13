//go:build !darwin && !linux

package supervisor

import (
	"context"

	"github.com/getbx/bx/internal/stats"
)

func collectNetworkWarnings(context.Context) []stats.Warning {
	return nil
}
