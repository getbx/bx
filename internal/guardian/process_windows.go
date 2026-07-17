//go:build windows

package guardian

import "errors"

func inspectProcess(int) (Process, error) {
	return Process{}, errors.New("Guardian process inspection is unsupported on Windows")
}
