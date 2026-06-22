package gateway

import "strings"

// FirewallPlan generates the fail-closed forwarding rules for router mode.
// Rules are inserted into the platform forward chain and tagged with Comment so
// teardown can locate and delete exactly them (by handle, at runtime).
//
// What it enforces:
//   - LAN → tun is ACCEPTED (else routed-to-tun packets hit fw4's default drop
//     and LAN clients get no internet).
//   - LAN IPv6 forwarding is DROPPED (a globally-unique v6 leaks the real IP via
//     ICE/WebRTC even behind a v4 proxy; dropping it forces clients onto v4→bx).
//
// The no-direct-WAN-leak property is enforced at the routing layer (RoutePlan's
// blackhole fallback), not here: LAN-sourced traffic is policy-routed to the tun
// table whose default is the tun or a blackhole, so it is never routed to the WAN.
type FirewallPlan struct {
	Table     string   // nft table spec, e.g. "inet fw4"
	Chain     string   // base forward chain, e.g. "forward"
	LANIfaces []string // LAN bridge ifaces, e.g. ["br-lan"]
	TunDev    string   // the bx tun device, e.g. "bx0"
	Comment   string   // tag attached to every rule for find/delete
}

func (p FirewallPlan) tableToks() []string { return strings.Fields(p.Table) }

func (p FirewallPlan) comment() []string { return []string{"comment", "\"" + p.Comment + "\""} }

// InstallRules returns nft argv lists (each minus the leading "nft").
func (p FirewallPlan) InstallRules() [][]string {
	var rules [][]string
	tbl := p.tableToks()
	for _, ifc := range p.LANIfaces {
		// LAN IPv6 forward → drop (insert first so it sits above the accept;
		// different nfproto so order is not strictly required, but explicit).
		r6 := append([]string{"insert", "rule"}, tbl...)
		r6 = append(r6, p.Chain, "iifname", ifc, "meta", "nfproto", "ipv6", "drop")
		r6 = append(r6, p.comment()...)
		rules = append(rules, r6)

		// LAN → tun (new connections) → accept. MUST be IPv4-only: both rules are
		// inserted at position 0, so this accept ends up ABOVE the IPv6 drop; without
		// the nfproto-ipv4 guard it would accept IPv6 into the tun and the drop above
		// becomes dead code (IPv6 leak). Return path is covered by fw4's existing
		// ct state established,related accept.
		r := append([]string{"insert", "rule"}, tbl...)
		r = append(r, p.Chain, "iifname", ifc, "meta", "nfproto", "ipv4", "oifname", p.TunDev, "accept")
		r = append(r, p.comment()...)
		rules = append(rules, r)
	}
	return rules
}

// TeardownMatch is the predicate the platform layer greps for in `nft -a list
// chain <table> <chain>` output to find the rule handles to delete.
func (p FirewallPlan) TeardownMatch() []string {
	return []string{"comment", "\"" + p.Comment + "\""}
}
