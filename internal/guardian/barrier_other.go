//go:build !darwin

package guardian

import (
	"context"
	"fmt"
)

type unsupportedBarrier struct{}

func NewBarrier(CommandRunner) Barrier {
	return unsupportedBarrier{}
}

func DiscoverDefaultGateway(context.Context) (string, error) {
	return "", fmt.Errorf("discover default gateway: %w", ErrUnsupported)
}

func (unsupportedBarrier) Install(context.Context, BarrierContext) error {
	return fmt.Errorf("install barrier: %w", ErrUnsupported)
}

func (unsupportedBarrier) ReassertBypass(context.Context, BarrierContext) error {
	return fmt.Errorf("reassert barrier bypass: %w", ErrUnsupported)
}

func (unsupportedBarrier) Release(context.Context, BarrierContext) error {
	return fmt.Errorf("release barrier to Core: %w", ErrUnsupported)
}

func (unsupportedBarrier) Remove(context.Context, BarrierContext) error {
	return fmt.Errorf("remove barrier: %w", ErrUnsupported)
}
