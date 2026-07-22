package supervisor

import (
	"context"
	"fmt"
	"net/netip"
	"sort"
	"strings"
)

// darwinUnderlayCommand is one route command in the recovery-only route plan.
// Fallback is limited to deleting and adding the exact same bypass prefix after
// route change reports that the old exact route is absent.
type darwinUnderlayCommand struct {
	Name          string
	Args          []string
	Fallback      []darwinUnderlayCommand
	IgnoreMissing bool
}

// darwinUnderlayPlan changes only bx-owned, underlay-dependent IPv4 bypasses.
// It intentionally does not call darwinRouteSpecs: that full-Hijack builder owns
// the capture routes and therefore cannot be part of recovery.
func darwinUnderlayPlan(old, next UnderlaySnapshot, serverBypass, userBypass []string) ([]darwinUnderlayCommand, error) {
	canonicalOld, err := canonicalUnderlaySnapshot(old.Interface, old.Gateway, old.LocalCIDRs)
	if err != nil {
		return nil, fmt.Errorf("invalid previous underlay: %w", err)
	}
	canonicalNext, err := canonicalUnderlaySnapshot(next.Interface, next.Gateway, next.LocalCIDRs)
	if err != nil {
		return nil, fmt.Errorf("invalid next underlay: %w", err)
	}
	if underlayGeneration(canonicalOld) == underlayGeneration(canonicalNext) {
		return nil, nil
	}

	prefixes, err := darwinUnderlayBypassPrefixes(serverBypass, userBypass)
	if err != nil {
		return nil, err
	}
	plan := make([]darwinUnderlayCommand, 0, len(prefixes))
	for _, prefix := range prefixes {
		plan = append(plan, darwinUnderlayChange(prefix, canonicalNext.Gateway.String()))
	}
	return plan, nil
}

func darwinUnderlayBypassPrefixes(serverBypass, userBypass []string) ([]string, error) {
	seen := make(map[string]struct{}, len(darwinDirectCIDRs)+len(serverBypass)+len(userBypass))
	add := func(raw string, exact bool) error {
		prefix, err := netip.ParsePrefix(raw)
		if err != nil || prefix != prefix.Masked() || !prefix.Addr().Is4() {
			return fmt.Errorf("invalid IPv4 underlay bypass %q", raw)
		}
		if exact && prefix.Bits() != 32 {
			return fmt.Errorf("server underlay bypass must be an exact IPv4 route: %q", raw)
		}
		if forbiddenDarwinUnderlayPrefix(prefix) {
			return fmt.Errorf("capture or catch-all route is not an underlay bypass: %q", raw)
		}
		seen[prefix.String()] = struct{}{}
		return nil
	}

	for _, prefix := range darwinDirectCIDRs {
		if err := add(prefix, false); err != nil {
			return nil, err
		}
	}
	for _, prefix := range serverBypass {
		if err := add(prefix, true); err != nil {
			return nil, err
		}
	}
	for _, prefix := range userBypass {
		if err := add(prefix, false); err != nil {
			return nil, err
		}
	}

	prefixes := make([]string, 0, len(seen))
	for prefix := range seen {
		prefixes = append(prefixes, prefix)
	}
	sort.Strings(prefixes)
	return prefixes, nil
}

func forbiddenDarwinUnderlayPrefix(prefix netip.Prefix) bool {
	if prefix.Bits() == 0 {
		return true
	}
	for _, capture := range []string{"0.0.0.0/1", "128.0.0.0/1"} {
		if prefix == netip.MustParsePrefix(capture) {
			return true
		}
	}
	return false
}

func darwinUnderlayChange(prefix, gateway string) darwinUnderlayCommand {
	return darwinUnderlayCommand{
		Name: "route",
		Args: []string{"-n", "change", "-net", prefix, gateway},
		Fallback: []darwinUnderlayCommand{
			{
				Name:          "route",
				Args:          []string{"-n", "delete", "-net", prefix},
				IgnoreMissing: true,
			},
			{
				Name: "route",
				Args: []string{"-n", "add", "-net", prefix, gateway},
			},
		},
	}
}

func executeDarwinUnderlayPlan(ctx context.Context, runner commandRunner, plan []darwinUnderlayCommand) error {
	for _, command := range plan {
		if err := runner.Run(ctx, command.Name, command.Args...); err == nil {
			continue
		} else if !darwinRouteMissing(err) {
			return underlayRebindFailed(command, err)
		}
		for _, fallback := range command.Fallback {
			err := runner.Run(ctx, fallback.Name, fallback.Args...)
			if err == nil || (fallback.IgnoreMissing && darwinRouteMissing(err)) {
				continue
			}
			return underlayRebindFailed(fallback, err)
		}
	}
	return nil
}

func darwinRouteMissing(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "not in table") || strings.Contains(message, "not found") || strings.Contains(message, "no such process")
}

func underlayRebindFailed(command darwinUnderlayCommand, err error) error {
	return &PathRecoveryError{
		Code:   "underlay_rebind_failed",
		Detail: fmt.Sprintf("%s %s: %v", command.Name, strings.Join(command.Args, " "), err),
	}
}
