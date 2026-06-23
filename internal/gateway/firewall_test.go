package gateway

import (
	"strings"
	"testing"
)

func fwplan() FirewallPlan {
	return FirewallPlan{
		Table:     "inet fw4",
		Chain:     "forward",
		LANIfaces: []string{"br-lan"},
		TunDev:    "bx0",
		Comment:   "bxr",
	}
}

// ruleWith reports whether toks appear as an adjacent run anywhere in any rule.
func ruleWith(rules [][]string, toks ...string) bool {
	for _, r := range rules {
		for start := 0; start+len(toks) <= len(r); start++ {
			ok := true
			for i, w := range toks {
				if r[start+i] != w {
					ok = false
					break
				}
			}
			if ok {
				return true
			}
		}
	}
	return false
}

// Every installed rule MUST carry the comment so teardown can find+delete it.
func TestFirewallRulesAreTagged(t *testing.T) {
	for _, r := range fwplan().InstallRules() {
		if !has(r, "comment") {
			t.Fatalf("rule not tagged with comment (unteardownable): %v", r)
		}
		joined := strings.Join(r, " ")
		if !strings.Contains(joined, "bxr") {
			t.Fatalf("rule missing comment tag bxr: %v", r)
		}
	}
}

// LAN → tun forwarding must be ACCEPTED (else routed-to-tun packets get dropped
// by fw4's default and LAN clients have no internet — the original outage).
func TestFirewallAllowsLANToTun(t *testing.T) {
	rules := fwplan().InstallRules()
	if !ruleWith(rules, "iifname", "br-lan") {
		t.Fatalf("no rule scoped to LAN iface: %v", rules)
	}
	found := false
	for _, r := range rules {
		j := strings.Join(r, " ")
		if strings.Contains(j, "br-lan") && strings.Contains(j, "bx0") && has(r, "accept") {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing LAN→tun accept rule: %v", rules)
	}
}

// IPv6 LAN forwarding must be DROPPED (globally-unique v6 leaks the real IP via
// ICE/WebRTC even behind a v4 proxy; force clients to fall back to v4 → bx).
func TestFirewallDropsLANIPv6(t *testing.T) {
	found := false
	for _, r := range fwplan().InstallRules() {
		j := strings.Join(r, " ")
		if strings.Contains(j, "br-lan") && strings.Contains(j, "ipv6") && has(r, "drop") {
			found = true
		}
	}
	if !found {
		t.Fatalf("MISSING LAN IPv6 drop — v6 leak vector open: %v", fwplan().InstallRules())
	}
}

// Multiple LAN ifaces each get covered.
func TestFirewallCoversAllLANIfaces(t *testing.T) {
	p := fwplan()
	p.LANIfaces = []string{"br-lan", "br-guest"}
	rules := p.InstallRules()
	for _, ifc := range p.LANIfaces {
		if !ruleWith(rules, "iifname", ifc) {
			t.Fatalf("LAN iface %s not covered: %v", ifc, rules)
		}
	}
}

func TestFirewallTeardownTargetsComment(t *testing.T) {
	// teardown must reference the comment so it deletes exactly our rules
	td := strings.Join(fwplan().TeardownMatch(), " ")
	if !strings.Contains(td, "bxr") {
		t.Fatalf("teardown match does not target the comment: %q", td)
	}
}

// The LAN→tun accept rule MUST be IPv4-only; otherwise (since both rules are
// inserted at position 0) the accept sits above the IPv6 drop and accepts v6
// into the tun, defeating the IPv6 leak prevention.
func TestFirewallAcceptIsIPv4Only(t *testing.T) {
	for _, r := range fwplan().InstallRules() {
		j := strings.Join(r, " ")
		if strings.Contains(j, "bx0") && has(r, "accept") {
			if !ruleWith([]([]string){r}, "nfproto", "ipv4") {
				t.Fatalf("LAN→tun accept rule is not IPv4-guarded — IPv6 can be accepted above the drop: %v", r)
			}
		}
	}
}

// IncludeRules (the fw4 chain-pre persistence) must carry the SAME fail-closed
// semantics as InstallRules: an IPv6 drop and a tagged LAN→tun IPv4 accept.
func TestIncludeRulesPreserveFailClosed(t *testing.T) {
	rules := fwplan().IncludeRules()
	if len(rules) == 0 {
		t.Fatal("IncludeRules emitted nothing — fw4 reload would leave LAN unprotected")
	}
	var v6drop, v4accept bool
	for _, r := range rules {
		if !strings.Contains(r, "bxr") {
			t.Fatalf("include rule not tagged (un-removable / un-idempotent): %q", r)
		}
		if strings.Contains(r, "ipv6") && strings.Contains(r, "drop") {
			v6drop = true
		}
		if strings.Contains(r, "ipv4") && strings.Contains(r, "bx0") && strings.Contains(r, "accept") {
			v4accept = true
		}
		// chain-pre files must be bare rule bodies — fw4 splices them inside the
		// chain, so an add/insert verb or table prefix would be a syntax error.
		if strings.HasPrefix(r, "add ") || strings.HasPrefix(r, "insert ") || strings.Contains(r, "fw4") {
			t.Fatalf("include rule must be a bare body (no verb/table), got: %q", r)
		}
	}
	if !v6drop {
		t.Error("IncludeRules missing IPv6 drop — fw4 reload would reopen the v6 leak")
	}
	if !v4accept {
		t.Error("IncludeRules missing LAN→tun IPv4 accept — fw4 reload would take LAN offline")
	}
}

// In a chain-pre file rules apply top-to-bottom, so the v6 drop must be emitted
// BEFORE the v4 accept (defensive; the nfproto guards already prevent crossover).
func TestIncludeRulesDropBeforeAccept(t *testing.T) {
	rules := fwplan().IncludeRules()
	dropIdx, acceptIdx := -1, -1
	for i, r := range rules {
		if dropIdx < 0 && strings.Contains(r, "ipv6") && strings.Contains(r, "drop") {
			dropIdx = i
		}
		if acceptIdx < 0 && strings.Contains(r, "accept") {
			acceptIdx = i
		}
	}
	if dropIdx < 0 || acceptIdx < 0 || dropIdx > acceptIdx {
		t.Fatalf("expected v6 drop before v4 accept, got drop@%d accept@%d in %v", dropIdx, acceptIdx, rules)
	}
}
