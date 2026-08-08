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

func BootoutGuardian(context.Context) error { return errGuardianUnsupported }

func GuardianLogTail(int) string { return "" }

func GuardianInstalled() bool { return false }

func GuardianActive() bool { return false }

// GuardianLoaded 在非 macOS 上没有 Guardian 可言:(false, nil) 是事实,不是
// 「问不出来」—— 调用方的 fail-closed 分支不该在这些平台上被触发。
func GuardianLoaded(context.Context) (bool, error) { return false, nil }

func LegacyCoreLoaded() (bool, error) { return false, nil }

func LegacyCoreInstalled() bool { return false }

func BootoutLegacyCoreUnit(context.Context) error { return errGuardianUnsupported }

func RemoveLegacyCoreUnit() error { return errGuardianUnsupported }
