//go:build darwin

package supervisor

import (
	"context"
	"os/exec"
	"regexp"
	"strings"

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

var darwinGuardTailscaleRouteRe = regexp.MustCompile(`(?m)^\s*(100\.64(?:\.0\.0)?/10|100\.100\.100\.100)\s+`)

func darwinHasTailscaleOverlayRoute(routes string) bool {
	return darwinGuardTailscaleRouteRe.MatchString(routes)
}

func darwinSystemProxyEnabled(scutilProxyOut string) bool {
	for _, line := range strings.Split(scutilProxyOut, "\n") {
		line = strings.TrimSpace(line)
		if line == "HTTPEnable : 1" || line == "HTTPSEnable : 1" || line == "SOCKSEnable : 1" {
			return true
		}
	}
	return false
}

var darwinGuardNetworkServiceLineRe = regexp.MustCompile(`^\*\s+\((Connected|Connecting)\)\s+(.+)$`)

// darwinGuardServiceDisplayNameRe 取 scutil 那一行里引号中的显示名。
var darwinGuardServiceDisplayNameRe = regexp.MustCompile(`"([^"]+)"`)

func darwinConnectedNetworkService(scutilNCListOut string) string {
	for _, line := range strings.Split(scutilNCListOut, "\n") {
		line = strings.TrimSpace(line)
		matches := darwinGuardNetworkServiceLineRe.FindStringSubmatch(line)
		if len(matches) != 3 {
			continue
		}
		tail := strings.TrimSpace(matches[2])
		// **这句话是常驻的**(`bx status` / `bx doctor` / 菜单三处),而 scutil
		// 那一行的尾巴是给列对齐用的:UUID、括号里的 bundle id、一长串填充空格,
		// 最后把同一个 bundle id 再印一遍。用户要的只有引号里那个显示名。
		if name := darwinGuardServiceDisplayNameRe.FindStringSubmatch(tail); len(name) == 2 {
			return name[1]
		}
		// 认不出显示名**绝不返回空串** —— 那会让「另一个 VPN 正开着」这条告警
		// 整个消失,而它正是这个函数存在的理由。退路是原文,只压掉多余空白。
		return strings.Join(strings.Fields(tail), " ")
	}
	return ""
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
