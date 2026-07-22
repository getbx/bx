//go:build !darwin

package supervisor

import "context"

type unavailableUnderlayManager struct{}

func newUnderlayManager() underlayManager { return unavailableUnderlayManager{} }

func (unavailableUnderlayManager) Observe(context.Context) (UnderlaySnapshot, error) {
	return UnderlaySnapshot{}, &PathRecoveryError{Code: "underlay_unavailable"}
}

func (unavailableUnderlayManager) ValidateCapture(context.Context, tunHandle) error {
	return &PathRecoveryError{Code: "underlay_unavailable"}
}

func (unavailableUnderlayManager) Rebind(context.Context, tunHandle, UnderlaySnapshot, UnderlaySnapshot, []string, []string) error {
	return &PathRecoveryError{Code: "underlay_unavailable"}
}
