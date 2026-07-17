//go:build !darwin

package guardian

import (
	"context"
	"fmt"
)

func requireDaemonPlatform() error {
	return fmt.Errorf("Guardian daemon: %w", ErrUnsupported)
}

func discoverDaemonGateway(context.Context) (string, error) {
	return "", fmt.Errorf("discover Guardian gateway: %w", ErrUnsupported)
}
