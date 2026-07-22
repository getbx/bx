//go:build darwin

package supervisor

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"os/exec"
	"strings"
)

type darwinUnderlayManager struct {
	runner            commandRunner
	defaultRoute      func(context.Context) (gateway, interfaceName string, err error)
	interfacePrefixes func(string) ([]netip.Prefix, error)
	routeLookup       func(context.Context, string, bool) (darwinRouteSelection, error)
	ipv6Enabled       func() bool
}

type darwinRouteSelection struct {
	Gateway   string
	Interface string
	Reject    bool
}

func newUnderlayManager() underlayManager { return newDarwinUnderlayManager() }

func newDarwinUnderlayManager() *darwinUnderlayManager {
	return &darwinUnderlayManager{
		runner:            darwinCommandRunner{},
		defaultRoute:      defaultRouteDarwinContext,
		interfacePrefixes: darwinInterfacePrefixes,
		routeLookup:       darwinRouteLookup,
		ipv6Enabled:       ipv6HostEnabled,
	}
}

func (m *darwinUnderlayManager) Observe(ctx context.Context) (UnderlaySnapshot, error) {
	if m.defaultRoute == nil || m.interfacePrefixes == nil {
		return UnderlaySnapshot{}, &PathRecoveryError{Code: "underlay_unavailable"}
	}
	gateway, interfaceName, err := m.defaultRoute(ctx)
	if err != nil {
		return UnderlaySnapshot{}, fmt.Errorf("observe default route: %w", err)
	}
	prefixes, err := m.interfacePrefixes(interfaceName)
	if err != nil {
		return UnderlaySnapshot{}, fmt.Errorf("observe interface %q addresses: %w", interfaceName, err)
	}
	parsedGateway, err := netip.ParseAddr(gateway)
	if err != nil {
		return UnderlaySnapshot{}, fmt.Errorf("parse underlay gateway %q: %w", gateway, err)
	}
	return newUnderlaySnapshot(interfaceName, parsedGateway, prefixes)
}

func (m *darwinUnderlayManager) ValidateCapture(ctx context.Context, tun tunHandle) error {
	if !strings.HasPrefix(tun.Name, "utun") || m.routeLookup == nil {
		return captureMissing("active bx utun is unavailable")
	}
	for _, destination := range []string{"1.1.1.1", "129.1.1.1"} {
		selection, err := m.routeLookup(ctx, destination, false)
		if err != nil {
			return captureMissing("query IPv4 capture for %s: %v", destination, err)
		}
		if selection.Interface != tun.Name {
			return captureMissing("IPv4 capture for %s selected %q, want %q", destination, selection.Interface, tun.Name)
		}
	}
	if m.ipv6Enabled != nil && m.ipv6Enabled() {
		for _, destination := range []string{"2001:4860:4860::8888", "9000::1"} {
			selection, err := m.routeLookup(ctx, destination, true)
			if err != nil {
				return captureMissing("query IPv6 capture for %s: %v", destination, err)
			}
			if !selection.Reject || selection.Gateway != "::1" {
				return captureMissing("IPv6 capture for %s is not bx reject", destination)
			}
		}
	}
	return nil
}

func (m *darwinUnderlayManager) Rebind(ctx context.Context, tun tunHandle, old, next UnderlaySnapshot, serverBypass, userBypass []string) error {
	if !strings.HasPrefix(tun.Name, "utun") || m.runner == nil {
		return &PathRecoveryError{Code: "underlay_rebind_failed", Detail: "active bx utun is unavailable"}
	}
	plan, err := darwinUnderlayPlan(old, next, serverBypass, userBypass)
	if err != nil {
		return &PathRecoveryError{Code: "underlay_rebind_failed", Detail: err.Error()}
	}
	return executeDarwinUnderlayPlan(ctx, m.runner, plan)
}

func darwinInterfacePrefixes(interfaceName string) ([]netip.Prefix, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	for _, iface := range interfaces {
		if iface.Name != interfaceName {
			continue
		}
		addresses, err := iface.Addrs()
		if err != nil {
			return nil, err
		}
		prefixes := make([]netip.Prefix, 0, len(addresses))
		for _, address := range addresses {
			ipNet, ok := address.(*net.IPNet)
			if !ok {
				continue
			}
			bits, _ := ipNet.Mask.Size()
			addr, ok := netip.AddrFromSlice(ipNet.IP)
			if !ok {
				continue
			}
			prefixes = append(prefixes, netip.PrefixFrom(addr, bits))
		}
		return prefixes, nil
	}
	return nil, fmt.Errorf("interface not found")
}

func darwinRouteLookup(ctx context.Context, destination string, ipv6 bool) (darwinRouteSelection, error) {
	args := []string{"-n", "get", destination}
	if ipv6 {
		args = []string{"-n", "get", "-inet6", destination}
	}
	out, err := exec.CommandContext(ctx, "route", args...).Output()
	if err != nil {
		return darwinRouteSelection{}, err
	}
	return parseDarwinRouteSelection(out)
}

func parseDarwinRouteSelection(output []byte) (darwinRouteSelection, error) {
	var selection darwinRouteSelection
	parsed := false
	for _, line := range strings.Split(string(output), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch strings.TrimSpace(key) {
		case "gateway":
			selection.Gateway = value
			parsed = true
		case "interface":
			selection.Interface = value
			parsed = true
		case "flags":
			selection.Reject = strings.Contains(strings.ToLower(value), "reject")
			parsed = true
		}
	}
	if !parsed {
		return darwinRouteSelection{}, fmt.Errorf("parse route selection: %q", strings.TrimSpace(string(output)))
	}
	return selection, nil
}

func captureMissing(format string, args ...any) error {
	return &PathRecoveryError{Code: "capture_missing", Detail: fmt.Sprintf(format, args...)}
}

type darwinCommandRunner struct{}

func (darwinCommandRunner) Run(ctx context.Context, name string, args ...string) error {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err == nil {
		return nil
	}
	if message := strings.TrimSpace(string(out)); message != "" {
		return fmt.Errorf("%w: %s", err, message)
	}
	return err
}
