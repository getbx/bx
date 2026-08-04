//go:build !darwin

package guardian

import (
	"fmt"
)

func requireDaemonPlatform() error {
	return fmt.Errorf("Guardian daemon: %w", ErrUnsupported)
}
