//go:build !darwin

package platformcheck

import "context"

func Collect(_ context.Context) []Check { return TerminalProxyChecks() }
