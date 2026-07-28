//go:build !darwin

package guardian

import "context"

type unsupportedNetworkEventSource struct{}

func (unsupportedNetworkEventSource) Events(context.Context) (<-chan struct{}, error) {
	return nil, errNetworkObserverUnsupported
}

type unsupportedUnderlayGenerationSource struct{}

func (unsupportedUnderlayGenerationSource) Current(context.Context) (string, error) {
	return "", errNetworkObserverUnsupported
}

func newPlatformNetworkObserver(networkRecoveryRequester) daemonNetworkObserver {
	return nil
}
