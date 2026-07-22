package supervisor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/netip"
	"sort"
	"strings"
)

// UnderlaySnapshot is the canonical physical path identity used during recovery.
// It deliberately excludes TUN state: capture is validated separately before any
// underlay-dependent route can change.
type UnderlaySnapshot struct {
	Generation string
	Interface  string
	Gateway    netip.Addr
	LocalCIDRs []netip.Prefix
}

// underlayManager owns only observation, capture validation, and physical bypass
// rebinding. It must never construct or remove capture routes.
type underlayManager interface {
	Observe(context.Context) (UnderlaySnapshot, error)
	ValidateCapture(context.Context, tunHandle) error
	Rebind(context.Context, tunHandle, UnderlaySnapshot, UnderlaySnapshot, []string, []string) error
}

// commandRunner keeps route execution injectable. Tests must use a fake runner;
// the Darwin implementation is the only production runner.
type commandRunner interface {
	Run(context.Context, string, ...string) error
}

func newUnderlaySnapshot(interfaceName string, gateway netip.Addr, localCIDRs []netip.Prefix) (UnderlaySnapshot, error) {
	snapshot, err := canonicalUnderlaySnapshot(interfaceName, gateway, localCIDRs)
	if err != nil {
		return UnderlaySnapshot{}, err
	}
	snapshot.Generation = underlayGeneration(snapshot)
	return snapshot, nil
}

func canonicalUnderlaySnapshot(interfaceName string, gateway netip.Addr, localCIDRs []netip.Prefix) (UnderlaySnapshot, error) {
	interfaceName = strings.TrimSpace(interfaceName)
	if !physicalUnderlayInterface(interfaceName) {
		return UnderlaySnapshot{}, fmt.Errorf("underlay interface %q is not physical", interfaceName)
	}

	gateway = gateway.Unmap()
	if !gateway.IsValid() || !gateway.Is4() || gateway.IsLoopback() || gateway.IsUnspecified() || gateway.IsMulticast() {
		return UnderlaySnapshot{}, fmt.Errorf("underlay gateway %q is not a physical IPv4 gateway", gateway)
	}

	prefixes := make([]netip.Prefix, 0, len(localCIDRs))
	seen := make(map[netip.Prefix]struct{}, len(localCIDRs))
	for _, prefix := range localCIDRs {
		prefix, err := canonicalUnderlayPrefix(prefix)
		if err != nil {
			return UnderlaySnapshot{}, err
		}
		if _, ok := seen[prefix]; ok {
			continue
		}
		seen[prefix] = struct{}{}
		prefixes = append(prefixes, prefix)
	}
	sort.Slice(prefixes, func(i, j int) bool { return prefixes[i].String() < prefixes[j].String() })

	return UnderlaySnapshot{
		Interface:  interfaceName,
		Gateway:    gateway,
		LocalCIDRs: prefixes,
	}, nil
}

func canonicalUnderlayPrefix(prefix netip.Prefix) (netip.Prefix, error) {
	if !prefix.IsValid() {
		return netip.Prefix{}, fmt.Errorf("invalid local underlay prefix %q", prefix)
	}
	if prefix.Addr().Is4In6() {
		if prefix.Bits() < 96 {
			return netip.Prefix{}, fmt.Errorf("invalid mapped IPv4 underlay prefix %q", prefix)
		}
		prefix = netip.PrefixFrom(prefix.Addr().Unmap(), prefix.Bits()-96)
	}
	return prefix.Masked(), nil
}

func physicalUnderlayInterface(interfaceName string) bool {
	if interfaceName == "" {
		return false
	}
	lower := strings.ToLower(interfaceName)
	return !strings.HasPrefix(lower, "utun") && !strings.HasPrefix(lower, "lo")
}

func underlayGeneration(snapshot UnderlaySnapshot) string {
	parts := make([]string, 0, len(snapshot.LocalCIDRs)+2)
	parts = append(parts, snapshot.Interface, snapshot.Gateway.String())
	for _, prefix := range snapshot.LocalCIDRs {
		parts = append(parts, prefix.String())
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:8])
}
