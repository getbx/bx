//go:build darwin

package guardian

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

const darwinRoutePath = "/sbin/route"

type darwinBarrier struct {
	runner CommandRunner
}

func NewBarrier(runner CommandRunner) Barrier {
	if runner == nil {
		runner = darwinRunner{}
	}
	return darwinBarrier{runner: runner}
}

func DiscoverDefaultGateway(ctx context.Context) (string, error) {
	output, err := (darwinRunner{}).Output(ctx, Command{Name: darwinRoutePath, Args: []string{"-n", "get", "default"}})
	if err != nil {
		return "", fmt.Errorf("discover default gateway: %w", err)
	}
	return parseDefaultGateway(output)
}

func (b darwinBarrier) Install(ctx context.Context, barrierCtx BarrierContext) error {
	apply, _, _, err := PlanBarrier(barrierCtx)
	if err != nil {
		return err
	}
	return b.run(ctx, apply, isRouteAlreadyExists)
}

func (b darwinBarrier) ReassertBypass(ctx context.Context, barrierCtx BarrierContext) error {
	_, reassert, _, err := PlanBarrier(barrierCtx)
	if err != nil {
		return err
	}
	return b.run(ctx, reassert, isRouteAlreadyExists)
}

func (b darwinBarrier) Remove(ctx context.Context, barrierCtx BarrierContext) error {
	_, _, cleanup, err := PlanBarrier(barrierCtx)
	if err != nil {
		return err
	}
	return b.run(ctx, cleanup, isRouteNotInTable)
}

func (b darwinBarrier) run(ctx context.Context, planned []Command, tolerated func(error) bool) error {
	for _, plan := range planned {
		command := Command{Name: darwinRoutePath, Args: append([]string(nil), plan.Args...)}
		if err := b.runner.Run(ctx, command); err != nil && !tolerated(err) {
			return fmt.Errorf("run %s: %w", command.String(), err)
		}
	}
	return nil
}

func isRouteAlreadyExists(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "file exists") || strings.Contains(strings.ToLower(err.Error()), "already exists")
}

func isRouteNotInTable(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "not in table")
}

type darwinRunner struct{}

func (darwinRunner) Run(ctx context.Context, command Command) error {
	_, err := (darwinRunner{}).Output(ctx, command)
	return err
}

func (darwinRunner) Output(ctx context.Context, command Command) ([]byte, error) {
	output, err := exec.CommandContext(ctx, command.Name, command.Args...).CombinedOutput()
	if err != nil {
		return output, commandOutputError{err: err, output: strings.TrimSpace(string(output))}
	}
	return output, nil
}

type commandOutputError struct {
	err    error
	output string
}

func (e commandOutputError) Error() string {
	if e.output == "" {
		return e.err.Error()
	}
	return e.err.Error() + ": " + e.output
}

func (e commandOutputError) Unwrap() error {
	return e.err
}
