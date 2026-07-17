//go:build !darwin

package guardian

import (
	"context"
	"errors"
	"testing"
)

func TestRunDaemonFailsClosedOnUnsupportedPlatform(t *testing.T) {
	err := RunDaemon(context.Background(), DaemonOptions{})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("RunDaemon error = %v, want ErrUnsupported", err)
	}
}
