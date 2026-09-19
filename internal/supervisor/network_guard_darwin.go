//go:build darwin

package supervisor

import (
	"context"
	"os/exec"
	"strings"

	"github.com/getbx/bx/internal/macnetprobe"

	"github.com/getbx/bx/internal/stats"
)

func collectNetworkWarnings(ctx context.Context) []stats.Warning {
	var warnings []stats.Warning
	if warning := darwinTailscaleWarning(ctx); warning.Name != "" {
		warnings = append(warnings, warning)
	}
	if warning := darwinSystemProxyWarning(ctx); warning.Name != "" {
		warnings = append(warnings, warning)
	}
	if warning := darwinPacketTunnelWarning(ctx); warning.Name != "" {
		warnings = append(warnings, warning)
	}
	return warnings
}

func darwinTailscaleWarning(ctx context.Context) stats.Warning {
	if !darwinAnyProcessDetected(ctx, []string{"Tailscale", "tailscaled"}) {
		return stats.Warning{}
	}
	routes, err := darwinReadCommand(ctx, "netstat", "-rn", "-f", "inet")
	if err != nil || darwinHasTailscaleOverlayRoute(routes) {
		return stats.Warning{}
	}
	return stats.Warning{
		Name:     "tailscale",
		Severity: "warn",
		Detail:   "Tailscale is running but its overlay route is not ready",
		Hint:     "wait for reconnect, or restart Tailscale after bx is on",
	}
}

func darwinSystemProxyWarning(ctx context.Context) stats.Warning {
	out, err := darwinReadCommand(ctx, "scutil", "--proxy")
	if err != nil || !darwinSystemProxyEnabled(out) {
		return stats.Warning{}
	}
	return stats.Warning{
		Name:     "system_proxy",
		Severity: "warn",
		Detail:   "macOS system proxy is enabled while bx is running",
		Hint:     "verify this proxy is intentional",
	}
}

func darwinPacketTunnelWarning(ctx context.Context) stats.Warning {
	out, err := darwinReadCommand(ctx, "scutil", "--nc", "list")
	if err != nil {
		return stats.Warning{}
	}
	if name := darwinConnectedNetworkService(out); name != "" {
		return stats.Warning{
			Name:     "packet_tunnel",
			Severity: "warn",
			Detail:   "macOS VPN service active: " + name,
			Hint:     "another VPN may own part of the network path",
		}
	}
	return stats.Warning{}
}

func darwinHasTailscaleOverlayRoute(routes string) bool {
	return macnetprobe.HasTailscaleOverlayRoute(routes)
}

func darwinSystemProxyEnabled(scutilProxyOut string) bool {
	return macnetprobe.SystemProxyEnabled(scutilProxyOut)
}

// darwinConnectedNetworkService 是 macnetprobe 的薄壳 —— **判定只有一份**。
// 此前这里与 internal/platformcheck 各有一份逐字拷贝,改了一处另一处不跟,于是
// `bx status` 与 `bx doctor` 对同一个事实说了两句不一样的话(2026-09-18 真机)。
func darwinConnectedNetworkService(scutilNCListOut string) string {
	return macnetprobe.ConnectedNetworkService(scutilNCListOut)
}

func darwinAnyProcessDetected(ctx context.Context, patterns []string) bool {
	for _, pattern := range patterns {
		if out, err := darwinReadCommand(ctx, "pgrep", "-fl", pattern); err == nil && strings.TrimSpace(out) != "" {
			return true
		}
	}
	return false
}

func darwinReadCommand(parent context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(parent, name, args...).CombinedOutput()
	if parent.Err() != nil {
		return "", parent.Err()
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
