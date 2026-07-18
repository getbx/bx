package guardian

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
)

var ErrUnsupported = errors.New("guardian barrier unsupported on this platform")

type BarrierContext struct {
	Gateway      string
	ServerBypass []string
	BlockIPv6    bool
	blockOnly    bool
}

type Command struct {
	Name string
	Args []string
}

func (c Command) String() string {
	parts := make([]string, 1, len(c.Args)+1)
	parts[0] = c.Name
	parts = append(parts, c.Args...)
	return strings.Join(parts, " ")
}

type CommandRunner interface {
	Run(context.Context, Command) error
}

type Barrier interface {
	Install(context.Context, BarrierContext) error
	ReassertBypass(context.Context, BarrierContext) error
	Release(context.Context, BarrierContext, []string) error
	Remove(context.Context, BarrierContext) error
}

type barrierRoute struct {
	add Command
	del Command
}

var (
	publicIPv4Blocks = []string{"0.0.0.0/2", "64.0.0.0/2", "128.0.0.0/2", "192.0.0.0/2"}
	publicIPv6Blocks = []string{"::/2", "4000::/2", "8000::/2", "c000::/2"}
)

func PlanBarrier(ctx BarrierContext) (apply, reassert, cleanup []Command, err error) {
	gateway, bypasses, err := validateBarrierContext(ctx)
	if err != nil {
		return nil, nil, nil, err
	}

	routes := make([]barrierRoute, 0, len(bypasses)+len(publicIPv4Blocks)+len(publicIPv6Blocks))
	for _, bypass := range bypasses {
		routes = append(routes, routeViaGateway(bypass, gateway))
	}
	routes = append(routes, blockingBarrierRoutes(ctx.BlockIPv6)...)

	apply = make([]Command, 0, len(routes))
	for _, route := range routes {
		apply = append(apply, route.add)
	}
	reassert = make([]Command, 0, len(bypasses))
	for _, bypass := range bypasses {
		reassert = append(reassert, routeViaGateway(bypass, gateway).add)
	}
	cleanup = make([]Command, 0, len(routes))
	for i := len(routes) - 1; i >= 0; i-- {
		cleanup = append(cleanup, routes[i].del)
	}
	return apply, reassert, cleanup, nil
}

func PlanBarrierRelease(ctx BarrierContext, transferredBypasses []string) ([]Command, error) {
	_, installedBypasses, err := validateBarrierContext(ctx)
	if err != nil {
		return nil, err
	}
	transferred, err := canonicalBypassSet(transferredBypasses)
	if err != nil {
		return nil, err
	}
	routes := blockingBarrierRoutes(ctx.BlockIPv6)
	release := make([]Command, 0, len(routes)+len(installedBypasses))
	for i := len(routes) - 1; i >= 0; i-- {
		release = append(release, routes[i].del)
	}
	for i := len(installedBypasses) - 1; i >= 0; i-- {
		if _, ok := transferred[installedBypasses[i]]; ok {
			continue
		}
		release = append(release, routeViaGateway(installedBypasses[i], ctx.Gateway).del)
	}
	return release, nil
}

func canonicalBypassSet(bypasses []string) (map[string]struct{}, error) {
	canonical := make(map[string]struct{}, len(bypasses))
	for _, bypass := range bypasses {
		prefix, err := netip.ParsePrefix(bypass)
		if err != nil || !prefix.Addr().Is4() || prefix.Bits() != 32 || prefix != prefix.Masked() {
			return nil, fmt.Errorf("invalid transferred IPv4 /32 server bypass %q", bypass)
		}
		canonical[prefix.String()] = struct{}{}
	}
	return canonical, nil
}

func blockingBarrierRoutes(blockIPv6 bool) []barrierRoute {
	routes := make([]barrierRoute, 0, len(publicIPv4Blocks)+len(publicIPv6Blocks))
	for _, block := range publicIPv4Blocks {
		routes = append(routes, rejectRoute(block, false))
	}
	if blockIPv6 {
		for _, block := range publicIPv6Blocks {
			routes = append(routes, rejectRoute(block, true))
		}
	}
	return routes
}

func routeViaGateway(cidr, gateway string) barrierRoute {
	return barrierRoute{
		add: Command{Name: "route", Args: []string{"-n", "add", "-net", cidr, gateway}},
		del: Command{Name: "route", Args: []string{"-n", "delete", "-net", cidr}},
	}
}

func rejectRoute(cidr string, ipv6 bool) barrierRoute {
	if ipv6 {
		return barrierRoute{
			add: Command{Name: "route", Args: []string{"-n", "add", "-inet6", "-net", cidr, "::1", "-reject"}},
			del: Command{Name: "route", Args: []string{"-n", "delete", "-inet6", "-net", cidr}},
		}
	}
	return barrierRoute{
		add: Command{Name: "route", Args: []string{"-n", "add", "-net", cidr, "127.0.0.1", "-reject"}},
		del: Command{Name: "route", Args: []string{"-n", "delete", "-net", cidr}},
	}
}

func validateBarrierContext(ctx BarrierContext) (string, []string, error) {
	if len(ctx.ServerBypass) == 0 {
		if ctx.blockOnly {
			return "", nil, nil
		}
		return "", nil, errors.New("server bypass required")
	}
	gateway, err := parseIPv4(ctx.Gateway)
	if err != nil {
		return "", nil, fmt.Errorf("invalid barrier gateway: %w", err)
	}

	bypasses := make([]string, 0, len(ctx.ServerBypass))
	seen := make(map[string]struct{}, len(ctx.ServerBypass))
	for _, bypass := range ctx.ServerBypass {
		prefix, err := netip.ParsePrefix(bypass)
		if err != nil || !prefix.Addr().Is4() || prefix.Bits() != 32 || prefix != prefix.Masked() {
			return "", nil, fmt.Errorf("invalid IPv4 /32 server bypass %q", bypass)
		}
		canonical := prefix.String()
		if _, duplicate := seen[canonical]; duplicate {
			continue
		}
		seen[canonical] = struct{}{}
		bypasses = append(bypasses, canonical)
	}
	return gateway.String(), bypasses, nil
}

func parseDefaultGateway(output []byte) (string, error) {
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "gateway:" {
			continue
		}
		gateway, err := parseIPv4(fields[1])
		if err != nil {
			return "", fmt.Errorf("invalid default gateway: %w", err)
		}
		return gateway.String(), nil
	}
	return "", errors.New("default gateway not found")
}

func parseIPv4(value string) (netip.Addr, error) {
	addr, err := netip.ParseAddr(value)
	if err != nil || !addr.Is4() || addr.IsUnspecified() || addr.IsMulticast() {
		return netip.Addr{}, fmt.Errorf("%q is not an IPv4 address", value)
	}
	return addr, nil
}
