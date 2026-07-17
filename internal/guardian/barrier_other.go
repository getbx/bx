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

func (unsupportedBarrier) Install(context.Context, BarrierContext) error {
	return fmt.Errorf("install barrier: %w", ErrUnsupported)
}

func (unsupportedBarrier) ReassertBypass(context.Context, BarrierContext) error {
	return fmt.Errorf("reassert barrier bypass: %w", ErrUnsupported)
}

func (unsupportedBarrier) Remove(context.Context, BarrierContext) error {
	return fmt.Errorf("remove barrier: %w", ErrUnsupported)
}
