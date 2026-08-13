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
	// **行为在前**:路由表能确定回答「有没有别的隧道在抢公网流量」,
	// 而下面那份产品清单只能回答「谁装了/在跑」。
	if routes, err := darwinCommand(ctx, "netstat", "-rn", "-f", "inet"); err == nil {
		checks = append(checks, darwinTunnelClaimChecks(routes, darwinBXTunName(ctx))...)
	} else {
		checks = append(checks, darwinTunnelClaimChecks("", "")...)
	}
	checks = append(checks, darwinCompetingTunnelChecks(ctx)...)
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

type darwinProcessDetector struct {
	name     string
	patterns []string
	status   string
	detail   string
	hint     string
}

// darwinCompetingTunnelChecks 回答「谁装了/在跑」,**不回答「谁在抢」** ——
// 后者由 darwinTunnelClaimChecks 从路由表得出。
//
// **它此前的措辞全是猜测**(「may create another tunnel」「verify its routes do not
// bypass bx」),让用户去查一件 bx 自己查得到的事。而产品名本来也分不出类:同一个
// 产品会随配置在叠加与竞争之间翻转(Tailscale 的 exit node、WireGuard 的 AllowedIPs)。
//
// 留着它的唯一理由:**进程在跑但还没连上时路由表里什么都没有** —— 那时这份清单是
// 唯一的线索。所以措辞改成如实说「它在跑」,并指向路由表要答案。
func darwinCompetingTunnelChecks(parent context.Context) []checkReport {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()

	var checks []checkReport
	for _, detector := range []darwinProcessDetector{
		{
			name:     "warp",
			patterns: []string{"Cloudflare WARP", "CloudflareWARP", "warp-svc"},
			status:   "info",
			detail:   "Cloudflare WARP is running",
			hint:     "whether it is taking traffic is answered by tunnel_claims (the routing table), not by this line",
		},
		{
			name:     "wireguard",
			patterns: []string{"WireGuard"},
			status:   "info",
			detail:   "WireGuard is running",
			hint:     "whether it is taking traffic is answered by tunnel_claims (the routing table); a WireGuard tunnel is only a competitor when its AllowedIPs cover public space",
		},
		{
			name:     "openvpn",
			patterns: []string{"OpenVPN", "openvpn"},
			status:   "info",
			detail:   "OpenVPN is running",
			hint:     "whether it is taking traffic is answered by tunnel_claims (the routing table); a split-tunnel OpenVPN coexists with bx",
		},
	} {
		if darwinAnyProcessDetected(ctx, detector.patterns) {
			checks = append(checks, checkReport{Name: detector.name, Status: detector.status, Detail: detector.detail, Hint: detector.hint})
		}
	}

	if check := darwinLocalProxyAppCheck(ctx); check.Name != "" {
		checks = append(checks, check)
	}
	if check := darwinPacketTunnelCheck(ctx); check.Name != "" {
		checks = append(checks, check)
	}
	return checks
}

func darwinLocalProxyAppCheck(ctx context.Context) checkReport {
	if !darwinAnyProcessDetected(ctx, []string{"Clash", "clash", "Surge", "surge", "mihomo"}) {
		return checkReport{}
	}
	proxyOut, err := darwinCommand(ctx, "scutil", "--proxy")
	if err != nil {
		return checkReport{Name: "local_proxy", Status: "info", Detail: "local proxy app detected; system proxy state was not inspected"}
	}
	if darwinSystemProxyEnabled(proxyOut) {
		return checkReport{
			Name:   "local_proxy",
			Status: "warn",
			Detail: "local proxy app detected and macOS system proxy is enabled",
			Hint:   "turn off the other proxy app's system proxy mode or verify app traffic with bx check --full",
		}
	}
	return checkReport{Name: "local_proxy", Status: "info", Detail: "local proxy app detected; macOS system proxy is off"}
}

func darwinPacketTunnelCheck(ctx context.Context) checkReport {
	ncOut, err := darwinCommand(ctx, "scutil", "--nc", "list")
	if err != nil {
		return checkReport{}
	}
	if name := darwinConnectedNetworkService(ncOut); name != "" {
		return checkReport{
			Name:   "packet_tunnel",
			Status: "warn",
			Detail: "macOS VPN service connected: " + name,
			Hint:   "whether it is taking public traffic is answered by tunnel_claims (the routing table); a split-tunnel VPN coexists with bx",
		}
	}
	return checkReport{}
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

var darwinNetworkServiceLineRe = regexp.MustCompile(`^\*\s+\((Connected|Connecting)\)\s+(.+)$`)

func darwinConnectedNetworkService(scutilNCListOut string) string {
	for _, line := range strings.Split(scutilNCListOut, "\n") {
		line = strings.TrimSpace(line)
		matches := darwinNetworkServiceLineRe.FindStringSubmatch(line)
		if len(matches) == 3 {
			return strings.TrimSpace(matches[2])
		}
	}
	return ""
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
