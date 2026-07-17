package guardian

import (
	"errors"
	"fmt"
	"net/netip"
)

// MigrationRequest is deliberately limited to non-secret route handoff data.
type MigrationRequest struct {
	Gateway      string   `json:"gateway"`
	ServerBypass []string `json:"server_bypass"`
}

func ValidateMigrationRequest(request MigrationRequest) (MigrationRequest, error) {
	gateway, err := parseIPv4(request.Gateway)
	if err != nil {
		return MigrationRequest{}, fmt.Errorf("invalid migration gateway: %w", err)
	}
	if len(request.ServerBypass) == 0 {
		return MigrationRequest{}, errors.New("migration server bypass required")
	}

	normalized := MigrationRequest{Gateway: gateway.String()}
	seen := make(map[string]struct{}, len(request.ServerBypass))
	hasIPv4 := false
	for _, value := range request.ServerBypass {
		prefix, err := netip.ParsePrefix(value)
		if err != nil || prefix != prefix.Masked() {
			return MigrationRequest{}, fmt.Errorf("migration bypass must be an exact IP prefix: %q", value)
		}
		addr := prefix.Addr().Unmap()
		switch {
		case addr.Is4() && prefix.Bits() == 32:
			prefix = netip.PrefixFrom(addr, 32)
			hasIPv4 = true
		case addr.Is6() && prefix.Bits() == 128:
			prefix = netip.PrefixFrom(addr, 128)
		default:
			return MigrationRequest{}, fmt.Errorf("migration bypass must be an exact IPv4 /32 or IPv6 /128: %q", value)
		}
		if addr.IsUnspecified() || addr.IsMulticast() {
			return MigrationRequest{}, fmt.Errorf("migration bypass is not a usable server address: %q", value)
		}
		canonical := prefix.String()
		if _, duplicate := seen[canonical]; duplicate {
			continue
		}
		seen[canonical] = struct{}{}
		normalized.ServerBypass = append(normalized.ServerBypass, canonical)
	}
	if !hasIPv4 {
		return MigrationRequest{}, errors.New("migration requires at least one IPv4 /32 server bypass")
	}
	return normalized, nil
}

func migrationBarrierContext(request MigrationRequest) BarrierContext {
	bypasses := make([]string, 0, len(request.ServerBypass))
	for _, value := range request.ServerBypass {
		prefix, err := netip.ParsePrefix(value)
		if err == nil && prefix.Addr().Is4() && prefix.Bits() == 32 {
			bypasses = append(bypasses, prefix.String())
		}
	}
	return BarrierContext{Gateway: request.Gateway, ServerBypass: bypasses, BlockIPv6: true}
}
