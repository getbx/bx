package gateway

import (
	"net/netip"
	"strings"
)

// IfaceCIDR pairs an interface name with one of its network CIDRs.
type IfaceCIDR struct {
	Name string
	CIDR string // network form, e.g. "192.168.8.0/24"
}

var privateV4 = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
}

// SelectLANCIDRs picks the LAN subnets from interface candidates, used when
// router.lan_cidrs is not set. Heuristic for OpenWrt-style routers: a LAN is a
// bridge interface (name starts "br-") carrying an RFC1918 private IPv4 network.
// This deliberately excludes the WAN (eth*/wlan*/wwan*) and non-private nets, so
// the router's uplink is never treated as a LAN to proxy. Result is deduped.
func SelectLANCIDRs(cands []IfaceCIDR) []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range cands {
		if !strings.HasPrefix(c.Name, "br-") {
			continue
		}
		p, err := netip.ParsePrefix(strings.TrimSpace(c.CIDR))
		if err != nil || !p.Addr().Is4() {
			continue
		}
		if !isPrivateV4(p.Addr()) {
			continue
		}
		net := p.Masked().String()
		if !seen[net] {
			seen[net] = true
			out = append(out, net)
		}
	}
	return out
}

func isPrivateV4(a netip.Addr) bool {
	for _, p := range privateV4 {
		if p.Contains(a) {
			return true
		}
	}
	return false
}
