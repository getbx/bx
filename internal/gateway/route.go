// Package gateway builds the routing + firewall plan for bx router mode:
// proxy BOTH the router's own traffic AND LAN-forwarded traffic, fail-closed,
// while coexisting with Tailscale and the OpenWrt/GL fwmark rules.
package gateway

import "strconv"

// Rule priorities. The catch-all sits AFTER Tailscale's rules (5210–5270) and
// GL's fwmark/LAN rules (6000/6500) so Tailscale's 0x80000 transport bypasses to
// direct (it works on corp) while everything unmarked (the router's own control
// traffic + LAN clients) is proxied — mirroring how mihomo coexists with Tailscale.
const (
	AntiLoopPref     = 100  // bx's own marked (fwMark) dials → main, before anything (anti-loop)
	ServerBypassPref = 6580 // brook server IP → main (so brook→server doesn't loop into the tun)
	CGNATPref        = 6589 // 100.64/10 → Tailscale table 52
	PrivatePref      = 6590 // RFC1918/docker/etc → main (direct, never tunneled)
	CatchAllPref     = 6600 // everything else (router-own + LAN-forwarded) → tun
)

const (
	tunMetric       = 1
	blackholeMetric = 1000
	tailscaleTable  = 52
	cgnatV4CIDR     = "100.64.0.0/10"
)

// RoutePlan describes router-mode policy routing as pure data; the args it emits
// are executed by the platform layer and printed by `bx router-plan`.
type RoutePlan struct {
	Table        int      // dedicated routing table whose default is the tun (+ blackhole fallback)
	TunDev       string   // the bx tun device, e.g. "bx0"
	FwMark       int      // bx's own-dial fwmark (anti-loop bypass), e.g. 0x162
	ServerBypass []string // brook server IP/CIDRs → direct (anti-loop)
	UserBypass   []string // user-configured direct CIDRs (management/admin nets)
	PrivateCIDRs []string // built-in private/docker/etc → direct
}

// InstallArgs returns argv lists for `ip` (v4). Order: table routes (default via
// tun + fail-closed blackhole), anti-loop fwmark bypass, server/private/CGNAT
// bypasses, then the catch-all into the tun.
func (p RoutePlan) InstallArgs() [][]string {
	t := strconv.Itoa(p.Table)
	var c [][]string
	// table: primary default via tun, plus a higher-metric blackhole so a dead
	// tun drops traffic (fail-closed) instead of falling through to a direct leak.
	c = append(c, []string{"route", "add", "default", "dev", p.TunDev, "table", t, "metric", strconv.Itoa(tunMetric)})
	c = append(c, []string{"route", "add", "blackhole", "default", "table", t, "metric", strconv.Itoa(blackholeMetric)})
	// anti-loop: bx's own marked dials go straight to main (never into the tun).
	c = append(c, []string{"rule", "add", "pref", strconv.Itoa(AntiLoopPref), "fwmark", fmtMark(p.FwMark), "table", "main"})
	// server bypass (highest priority among the dst rules): brook→server stays direct.
	for _, b := range p.ServerBypass {
		c = append(c, []string{"rule", "add", "to", b, "pref", strconv.Itoa(ServerBypassPref), "table", "main"})
	}
	for _, b := range p.UserBypass {
		c = append(c, []string{"rule", "add", "to", b, "pref", strconv.Itoa(ServerBypassPref), "table", "main"})
	}
	// private/docker → direct; CGNAT (Tailscale overlay) → table 52.
	for _, cidr := range p.PrivateCIDRs {
		if cidr == cgnatV4CIDR {
			c = append(c, []string{"rule", "add", "to", cidr, "pref", strconv.Itoa(CGNATPref), "table", strconv.Itoa(tailscaleTable)})
		}
		c = append(c, []string{"rule", "add", "to", cidr, "pref", strconv.Itoa(PrivatePref), "table", "main"})
	}
	// catch-all: router-own + LAN-forwarded → tun. After Tailscale/GL rules.
	c = append(c, []string{"rule", "add", "pref", strconv.Itoa(CatchAllPref), "table", t})
	return c
}

// TeardownArgs reverses InstallArgs: drop the rules, then flush the table.
func (p RoutePlan) TeardownArgs() [][]string {
	t := strconv.Itoa(p.Table)
	var c [][]string
	c = append(c, []string{"rule", "del", "pref", strconv.Itoa(CatchAllPref), "table", t})
	for _, cidr := range p.PrivateCIDRs {
		if cidr == cgnatV4CIDR {
			c = append(c, []string{"rule", "del", "to", cidr, "pref", strconv.Itoa(CGNATPref), "table", strconv.Itoa(tailscaleTable)})
		}
		c = append(c, []string{"rule", "del", "to", cidr, "pref", strconv.Itoa(PrivatePref), "table", "main"})
	}
	for _, b := range append(append([]string{}, p.ServerBypass...), p.UserBypass...) {
		c = append(c, []string{"rule", "del", "to", b, "pref", strconv.Itoa(ServerBypassPref), "table", "main"})
	}
	c = append(c, []string{"rule", "del", "pref", strconv.Itoa(AntiLoopPref), "fwmark", fmtMark(p.FwMark), "table", "main"})
	c = append(c, []string{"route", "flush", "table", t})
	return c
}

func fmtMark(m int) string {
	return "0x" + strconv.FormatInt(int64(m), 16)
}
