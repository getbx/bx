//go:build darwin

package guardian

import "context"

func requireDaemonPlatform() error {
	return nil
}

func discoverDaemonGateway(ctx context.Context) (string, error) {
	return DiscoverDefaultGateway(ctx)
}
