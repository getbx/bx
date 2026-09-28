//go:build !darwin

package supervisor

import "context"

func runPFReset(context.Context, string, []string, []string) {}

func flushStalePFReset() {}
