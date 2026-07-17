package supervisor

import (
	"net/netip"
	"sync/atomic"
)

// RuntimeState is the non-secret handoff contract consumed by Guardian.
type RuntimeState struct {
	Version         string   `json:"version"`
	PID             int      `json:"pid"`
	TunName         string   `json:"tun_name"`
	SocksAddr       string   `json:"socks_addr"`
	ServerBypass    []string `json:"server_bypass"`
	TunnelHealthy   bool     `json:"tunnel_healthy"`
	DNSListening    bool     `json:"dns_listening"`
	RoutesInstalled bool     `json:"routes_installed"`
	UDPRequired     bool     `json:"udp_required"`
	UDPReady        bool     `json:"udp_ready"`
}

type routeReadiness struct {
	installed atomic.Bool
}

func (r *routeReadiness) set(installed bool) {
	r.installed.Store(installed)
}

func (r *routeReadiness) ready() bool {
	return r.installed.Load()
}

func runtimeIPv4Bypass(addrs []netip.Addr) []string {
	seen := make(map[string]struct{}, len(addrs))
	out := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		addr = addr.Unmap()
		if !addr.Is4() {
			continue
		}
		cidr := netip.PrefixFrom(addr, 32).String()
		if _, ok := seen[cidr]; ok {
			continue
		}
		seen[cidr] = struct{}{}
		out = append(out, cidr)
	}
	return out
}

func udpRuntimeReadiness(mode string, primaryHealthy, companionHealthy func() bool) (required, ready bool) {
	if mode != "proxy" {
		return false, false
	}
	if companionHealthy != nil {
		return true, companionHealthy()
	}
	return true, primaryHealthy != nil && primaryHealthy()
}
