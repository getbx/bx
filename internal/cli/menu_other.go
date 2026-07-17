//go:build !darwin

package cli

import "fmt"

func ensureMacOSMenuRunning(int) error {
	return fmt.Errorf("macOS menu bootstrap is unsupported on this platform")
}

func consoleUserUID() (int, error) {
	return 0, fmt.Errorf("macOS console user lookup is unsupported on this platform")
}
