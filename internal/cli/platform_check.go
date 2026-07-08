//go:build !darwin

package cli

import "context"

func collectPlatformChecks(ctx context.Context) []checkReport {
	return collectTerminalProxyChecks()
}
