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
