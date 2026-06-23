package gateway

import "testing"

func argsContain(cmds [][]string, want ...string) bool {
	for _, c := range cmds {
		for start := 0; start+len(want) <= len(c); start++ {
			ok := true
			for i, w := range want {
				if c[start+i] != w {
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

func samplePlan() RoutePlan {
	return RoutePlan{
		Table:        441,
		TunDev:       "bx0",
		FwMark:       0x162,
		ServerBypass: []string{"203.0.113.10/32"},
		UserBypass:   []string{"192.168.50.0/24"},
		PrivateCIDRs: []string{"10.0.0.0/8", "192.168.0.0/16", "100.64.0.0/10"},
	}
}

// Fail-closed: a blackhole default at a HIGHER metric than the tun default, so a
// dead tun drops traffic instead of leaking it.
func TestBlackholeFailClosed(t *testing.T) {
	cmds := samplePlan().InstallArgs()
	if !argsContain(cmds, "route", "add", "default", "dev", "bx0", "table", "441") {
		t.Fatalf("missing tun default: %v", cmds)
	}
	if !argsContain(cmds, "route", "add", "blackhole", "default", "table", "441") {
		t.Fatalf("MISSING fail-closed blackhole — leak risk if tun dies: %v", cmds)
	}
	tunM, blackM := metricOf(cmds, "dev", "bx0"), metricOf(cmds, "blackhole", "default")
	if !(blackM > tunM) || tunM < 0 {
		t.Fatalf("blackhole metric %d must be > tun metric %d", blackM, tunM)
	}
}

// The catch-all MUST sit after Tailscale's rules (5210–5270) so 0x80000 transport
// bypasses to direct; otherwise bx swallows Tailscale and breaks it.
func TestCatchAllAfterTailscale(t *testing.T) {
	if CatchAllPref <= 5270 {
		t.Fatalf("CatchAllPref %d must be > 5270 (Tailscale rules) to coexist", CatchAllPref)
	}
	if !argsContain(samplePlan().InstallArgs(), "rule", "add", "pref", "6600", "table", "441") {
		t.Fatalf("missing catch-all into tun at pref 6600")
	}
}

func TestServerBypassDirect(t *testing.T) {
	// brook→server must bypass the tun (anti-loop) → main, before the catch-all.
	if !argsContain(samplePlan().InstallArgs(), "rule", "add", "to", "203.0.113.10/32", "pref", "6580", "table", "main") {
		t.Fatalf("missing server bypass to main")
	}
	if !(ServerBypassPref < CatchAllPref) {
		t.Fatalf("server bypass pref must be < catch-all")
	}
}

func TestAntiLoopFwmark(t *testing.T) {
	if !argsContain(samplePlan().InstallArgs(), "rule", "add", "pref", "100", "fwmark", "0x162", "table", "main") {
		t.Fatalf("missing anti-loop fwmark bypass")
	}
}

func TestPrivateAndCGNAT(t *testing.T) {
	cmds := samplePlan().InstallArgs()
	if !argsContain(cmds, "rule", "add", "to", "10.0.0.0/8", "pref", "6590", "table", "main") {
		t.Fatalf("private 10/8 should go direct to main: %v", cmds)
	}
	if !argsContain(cmds, "rule", "add", "to", "100.64.0.0/10", "pref", "6589", "table", "52") {
		t.Fatalf("CGNAT should go to tailscale table 52: %v", cmds)
	}
}

func TestTeardownMirrors(t *testing.T) {
	cmds := samplePlan().TeardownArgs()
	if !argsContain(cmds, "rule", "del", "pref", "6600", "table", "441") {
		t.Fatalf("teardown missing catch-all del")
	}
	if !argsContain(cmds, "route", "flush", "table", "441") {
		t.Fatalf("teardown missing table flush")
	}
	if !argsContain(cmds, "rule", "del", "to", "203.0.113.10/32", "pref", "6580", "table", "main") {
		t.Fatalf("teardown missing server bypass del")
	}
}

// --- helpers ---
func metricOf(cmds [][]string, mustHave ...string) int {
	for _, c := range cmds {
		ok := true
		for _, m := range mustHave {
			found := false
			for _, x := range c {
				if x == m {
					found = true
					break
				}
			}
			if !found {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		for i, x := range c {
			if x == "metric" && i+1 < len(c) {
				n := 0
				for _, ch := range c[i+1] {
					if ch < '0' || ch > '9' {
						break
					}
					n = n*10 + int(ch-'0')
				}
				return n
			}
		}
	}
	return -1
}

func has(c []string, tok string) bool {
	for _, x := range c {
		if x == tok {
			return true
		}
	}
	return false
}

func TestV6FailClosed(t *testing.T) {
	p := samplePlan()
	p.PrivateV6CIDRs = []string{"fe80::/10", "fc00::/7"}
	cmds := p.InstallArgs()
	if !argsContain(cmds, "-6", "route", "add", "unreachable", "default", "table", "441") {
		t.Fatalf("missing v6 unreachable default (router-own v6 must fail → fall back to v4): %v", cmds)
	}
	if !argsContain(cmds, "-6", "rule", "add", "pref", "6600", "table", "441") {
		t.Fatalf("missing v6 catch-all")
	}
	if !argsContain(cmds, "-6", "rule", "add", "to", "fe80::/10", "pref", "6590", "table", "main") {
		t.Fatalf("missing v6 private carve-out")
	}
}

func TestNoV6WhenDisabled(t *testing.T) {
	// empty PrivateV6CIDRs → no v6 args (don't touch v6 if router has none)
	for _, c := range samplePlan().InstallArgs() {
		if len(c) > 0 && c[0] == "-6" {
			t.Fatalf("unexpected v6 args when PrivateV6CIDRs empty: %v", c)
		}
	}
}
