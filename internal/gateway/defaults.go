package gateway

// Default router-mode parameters, shared by the platform layer (which executes
// the plans) and the `bx router-plan` dry-run command (which prints them), so
// the printed plan always matches what would actually be applied.
const (
	DefaultTable    = 441          // LAN-forward routing table (avoids mihomo 1001 / tailscale 52)
	DefaultRulePref = 6500         // ip rule priority for the LAN source rules
	DefaultComment  = "bxr"        // fw4 rule tag for handle-based teardown
	DefaultFwTable  = "inet fw4"   // OpenWrt nftables table
	DefaultFwChain  = "forward"    // base forward chain
)

// DefaultRoutePlan builds the standard router-mode route plan.
func DefaultRoutePlan(tunDev string, lanCIDRs []string) RoutePlan {
	return RoutePlan{Table: DefaultTable, TunDev: tunDev, RulePref: DefaultRulePref, LANCIDRs: lanCIDRs}
}

// DefaultFirewallPlan builds the standard router-mode firewall plan.
func DefaultFirewallPlan(tunDev string, lanIfaces []string) FirewallPlan {
	return FirewallPlan{Table: DefaultFwTable, Chain: DefaultFwChain, LANIfaces: lanIfaces, TunDev: tunDev, Comment: DefaultComment}
}
