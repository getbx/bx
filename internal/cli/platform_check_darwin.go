//go:build darwin

package cli

import (
	"context"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

func collectPlatformChecks(ctx context.Context) []checkReport {
	checks := collectTerminalProxyChecks()
	if check := darwinTailscaleCheck(ctx); check.Name != "" {
		checks = append(checks, check)
	}
	if check := darwinZeroTierCheck(ctx); check.Name != "" {
		checks = append(checks, check)
	}
	return checks
}

func darwinTailscaleCheck(parent context.Context) checkReport {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()

	if !darwinTailscaleProcessDetected(ctx) {
		return checkReport{}
	}
	routes, err := darwinCommand(ctx, "netstat", "-rn", "-f", "inet")
	if err != nil {
		return checkReport{Name: "tailscale", Status: "warn", Detail: "could not inspect routes: " + err.Error()}
	}
	if darwinHasTailscaleOverlayRoute(routes) {
		return checkReport{Name: "tailscale", Status: "ok", Detail: "overlay route present"}
	}
	routeGet, err := darwinCommand(ctx, "route", "-n", "get", "100.100.100.100")
	if err != nil {
		return checkReport{Name: "tailscale", Status: "warn", Detail: "installed/running, overlay route not visible"}
	}
	if iface := darwinRouteGetInterface(routeGet); strings.HasPrefix(iface, "utun") {
		return checkReport{
			Name:   "tailscale",
			Status: "warn",
			Detail: "installed/running, but Tailscale 100.x route is absent and traffic currently follows " + iface,
			Hint:   "wait for Tailscale to reconnect, then run bx leak-check --json",
		}
	}
	return checkReport{Name: "tailscale", Status: "warn", Detail: "installed/running, overlay route not visible"}
}

func darwinTailscaleProcessDetected(ctx context.Context) bool {
	return darwinAnyProcessDetected(ctx, []string{"Tailscale", "tailscaled"})
}

var darwinTailscaleRouteRe = regexp.MustCompile(`(?m)^\s*(100\.64(?:\.0\.0)?/10|100\.100\.100\.100)\s+`)

func darwinHasTailscaleOverlayRoute(routes string) bool {
	return darwinTailscaleRouteRe.MatchString(routes)
}

func darwinRouteGetInterface(out string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "interface:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "interface:"))
		}
	}
	return ""
}

func darwinZeroTierCheck(parent context.Context) checkReport {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()

	if !darwinAnyProcessDetected(ctx, []string{"ZeroTier", "zerotier-one"}) {
		return checkReport{}
	}
	ifaces, err := darwinCommand(ctx, "ifconfig")
	if err != nil {
		return checkReport{
			Name:   "zerotier",
			Status: "info",
			Detail: "detected, but interface state was not inspected: " + err.Error(),
		}
	}
	if darwinHasZeroTierInterface(ifaces) {
		return checkReport{Name: "zerotier", Status: "ok", Detail: "overlay interface present"}
	}
	return checkReport{
		Name:   "zerotier",
		Status: "info",
		Detail: "detected; managed routes are app/network specific and not owned by bx",
		Hint:   "if ZeroTier cannot connect, restart it after bx is on",
	}
}

var darwinZeroTierInterfaceRe = regexp.MustCompile(`(?mi)^(zt[a-z0-9]+|feth[0-9]+):\s+flags=`)

func darwinHasZeroTierInterface(ifconfigOut string) bool {
	return darwinZeroTierInterfaceRe.MatchString(ifconfigOut) || strings.Contains(strings.ToLower(ifconfigOut), "zerotier")
}

func darwinAnyProcessDetected(ctx context.Context, patterns []string) bool {
	for _, pattern := range patterns {
		if out, err := darwinCommand(ctx, "pgrep", "-fl", pattern); err == nil && strings.TrimSpace(out) != "" {
			return true
		}
	}
	return false
}

func darwinCommand(parent context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(parent, name, args...).CombinedOutput()
	if parent.Err() != nil {
		return "", parent.Err()
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
