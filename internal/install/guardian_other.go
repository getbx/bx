//go:build !darwin

package install

import (
	"context"
	"errors"
)

var errGuardianUnsupported = errors.New("macOS Guardian installation is unsupported on this platform")

func WriteGuardianUnit(string, string) error { return errGuardianUnsupported }

func EnableGuardian() error { return errGuardianUnsupported }

func EnableGuardianWithProbe(func() bool) error { return errGuardianUnsupported }

func GuardianLogTail(int) string { return "" }

func GuardianInstalled() bool { return false }

func GuardianActive() bool { return false }

func LegacyCoreLoaded() (bool, error) { return false, nil }

func LegacyCoreInstalled() bool { return false }

func BootoutLegacyCoreUnit(context.Context) error { return errGuardianUnsupported }

func RemoveLegacyCoreUnit() error { return errGuardianUnsupported }
