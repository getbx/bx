package gateway

// Default router-mode parameters, shared by the platform layer (which executes
// the plans) and the `bx router-plan` dry-run command (which prints them), so
// the printed plan always matches what would actually be applied.
const (
	DefaultTable   = 441        // routing table whose default is the tun (+ blackhole)
	DefaultFwMark  = 0x162      // bx's own-dial fwmark (anti-loop), matches supervisor fwMark
	DefaultComment = "bxr"      // fw4 rule tag for handle-based teardown
	DefaultFwTable = "inet fw4" // OpenWrt nftables table
	DefaultFwChain = "forward"  // base forward chain
)

// DefaultRoutePlan builds the standard router-mode route plan. privateCIDRs are
// the built-in always-direct nets (RFC1918/docker/CGNAT); serverBypass is the
// brook server IP(s); userBypass is the config's bypass list.
func DefaultRoutePlan(tunDev string, serverBypass, userBypass, privateCIDRs, privateV6CIDRs []string) RoutePlan {
	return RoutePlan{
		Table:          DefaultTable,
		TunDev:         tunDev,
		FwMark:         DefaultFwMark,
		ServerBypass:   serverBypass,
		UserBypass:     userBypass,
		PrivateCIDRs:   privateCIDRs,
		PrivateV6CIDRs: privateV6CIDRs,
	}
}

// DefaultFirewallPlan builds the standard router-mode firewall plan.
func DefaultFirewallPlan(tunDev string, lanIfaces []string) FirewallPlan {
	return FirewallPlan{Table: DefaultFwTable, Chain: DefaultFwChain, LANIfaces: lanIfaces, TunDev: tunDev, Comment: DefaultComment}
}
