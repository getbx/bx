//go:build windows

package guardian

import "fmt"

func statExecutableIdentity(string) (executableIdentity, error) {
	return executableIdentity{}, fmt.Errorf("Core executable identity: %w", ErrUnsupported)
}
