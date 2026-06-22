package gateway

import "testing"

func argsContain(cmds [][]string, want ...string) bool {
	for _, c := range cmds {
		if len(c) < len(want) {
			continue
		}
		ok := true
		for i, w := range want {
			if c[i] != w {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func plan() RoutePlan {
	return RoutePlan{
		Table:    447,
		TunDev:   "bx0",
		RulePref: 6500,
		LANCIDRs: []string{"192.168.8.0/24", "10.20.0.0/24"},
	}
}

func TestInstallRulePerLANCIDR(t *testing.T) {
	cmds := plan().InstallArgs()
	for _, cidr := range []string{"192.168.8.0/24", "10.20.0.0/24"} {
		if !argsContain(cmds, "rule", "add", "from", cidr, "lookup", "447") {
			t.Fatalf("missing source rule for %s in %v", cidr, cmds)
		}
	}
}

func TestInstallDefaultViaTun(t *testing.T) {
	cmds := plan().InstallArgs()
	if !argsContain(cmds, "route", "add", "default", "dev", "bx0", "table", "447") {
		t.Fatalf("missing default-via-tun route in %v", cmds)
	}
}

// Fail-closed invariant: the table MUST contain a blackhole default so that
// when bx0 disappears (bx down), LAN traffic is dropped, never leaked to WAN.
func TestInstallHasBlackholeFailClosed(t *testing.T) {
	cmds := plan().InstallArgs()
	if !argsContain(cmds, "route", "add", "blackhole", "default", "table", "447") {
		t.Fatalf("MISSING fail-closed blackhole default — LAN could leak to WAN if bx0 dies: %v", cmds)
	}
}

// The blackhole must have a HIGHER metric than the tun default, so the tun
// route wins while up and the blackhole only takes over when the tun is gone.
func TestBlackholeMetricHigherThanTun(t *testing.T) {
	tunM, blackM := -1, -1
	for _, c := range plan().InstallArgs() {
		s := join(c)
		if has(c, "default") && has(c, "dev") && has(c, "bx0") {
			tunM = metric(c)
		}
		if has(c, "blackhole") && has(c, "default") {
			blackM = metric(c)
		}
		_ = s
	}
	if tunM < 0 || blackM < 0 {
		t.Fatalf("could not find both routes")
	}
	if !(blackM > tunM) {
		t.Fatalf("blackhole metric %d must be > tun metric %d (else fail-open)", blackM, tunM)
	}
}

func TestTeardownMirrorsInstallWithDel(t *testing.T) {
	cmds := plan().TeardownArgs()
	if !argsContain(cmds, "rule", "del", "from", "192.168.8.0/24", "lookup", "447") {
		t.Fatalf("teardown missing rule del: %v", cmds)
	}
	if !argsContain(cmds, "route", "flush", "table", "447") && !argsContain(cmds, "route", "del", "default", "dev", "bx0", "table", "447") {
		t.Fatalf("teardown does not remove table routes: %v", cmds)
	}
}

func TestNoLANCIDRsProducesNoRules(t *testing.T) {
	p := plan()
	p.LANCIDRs = nil
	cmds := p.InstallArgs()
	for _, c := range cmds {
		if has(c, "rule") {
			t.Fatalf("expected no ip rules with empty LANCIDRs, got %v", cmds)
		}
	}
}

// --- small test helpers ---
func has(c []string, tok string) bool {
	for _, x := range c {
		if x == tok {
			return true
		}
	}
	return false
}
func metric(c []string) int {
	for i, x := range c {
		if x == "metric" && i+1 < len(c) {
			n := 0
			for _, ch := range c[i+1] {
				n = n*10 + int(ch-'0')
			}
			return n
		}
	}
	return -1
}
func join(c []string) string {
	s := ""
	for _, x := range c {
		s += x + " "
	}
	return s
}
