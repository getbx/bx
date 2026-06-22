// Package gateway builds the routing + firewall plan for bx router mode:
// proxy only LAN-forwarded traffic (matched by source CIDR), fail-closed so a
// dead tunnel drops LAN traffic rather than leaking it to the WAN.
package gateway

import "strconv"

const (
	tunMetric       = 1
	blackholeMetric = 1000
)

// RoutePlan describes the policy-routing for router mode. Pure data → the args
// it emits are executed by the platform layer; no side effects here (testable).
type RoutePlan struct {
	Table    int      // dedicated routing table for LAN-forwarded traffic
	TunDev   string   // the bx tun device, e.g. "bx0"
	RulePref int      // ip rule priority for the source rules
	LANCIDRs []string // source nets whose forwarded traffic is hijacked into the tun
}

// InstallArgs returns argv lists for `ip` (v4) that:
//   - add a default route via the tun in the dedicated table (primary), and
//   - add a blackhole default in the same table at a HIGHER metric, so that
//     when the tun device disappears (bx down) LAN traffic is dropped — never
//     leaked out the WAN (fail-closed), and
//   - add one source rule per LAN CIDR steering its traffic into that table.
func (p RoutePlan) InstallArgs() [][]string {
	t := strconv.Itoa(p.Table)
	var cmds [][]string
	// table routes first so the table exists before rules point at it
	cmds = append(cmds, []string{"route", "add", "default", "dev", p.TunDev, "table", t, "metric", strconv.Itoa(tunMetric)})
	cmds = append(cmds, []string{"route", "add", "blackhole", "default", "table", t, "metric", strconv.Itoa(blackholeMetric)})
	for _, cidr := range p.LANCIDRs {
		cmds = append(cmds, []string{"rule", "add", "from", cidr, "lookup", t, "pref", strconv.Itoa(p.RulePref)})
	}
	return cmds
}

// TeardownArgs reverses InstallArgs: drop the source rules, then flush the table.
func (p RoutePlan) TeardownArgs() [][]string {
	t := strconv.Itoa(p.Table)
	var cmds [][]string
	for _, cidr := range p.LANCIDRs {
		cmds = append(cmds, []string{"rule", "del", "from", cidr, "lookup", t, "pref", strconv.Itoa(p.RulePref)})
	}
	cmds = append(cmds, []string{"route", "flush", "table", t})
	return cmds
}
