package supervisor

import "testing"

func TestTailscaleDERPMapBypassCIDRs(t *testing.T) {
	derpMap := []byte(`{
	  "Regions": {
	    "1": {"Nodes": [
	      {"Name": "1a", "IPv4": "203.0.113.10", "IPv6": "2001:db8::10"},
	      {"Name": "1b", "IPv4": "203.0.113.11"}
	    ]},
	    "2": {"Nodes": [
	      {"Name": "2a", "IPv4": "203.0.113.10"}
	    ]}
	  }
	}`)
	got := tailscaleDERPMapBypassCIDRs(derpMap)
	want := []string{"203.0.113.10/32", "203.0.113.11/32"}
	if !sameStringSet(got, want) {
		t.Fatalf("tailscale DERP bypass CIDRs = %v, want %v", got, want)
	}
}

func TestMergeBypassCIDRsDedupes(t *testing.T) {
	got := mergeBypassCIDRs(
		[]string{"23.27.134.77/32", "203.0.113.10/32"},
		[]string{"203.0.113.10/32", "203.0.113.11/32"},
	)
	want := []string{"23.27.134.77/32", "203.0.113.10/32", "203.0.113.11/32"}
	if !sameStringSet(got, want) || len(got) != len(want) {
		t.Fatalf("merged bypass CIDRs = %v, want %v", got, want)
	}
}

func TestTailscaleControlplaneFallbackCIDRs(t *testing.T) {
	got := tailscaleControlplaneFallbackCIDRs()
	for _, want := range []string{"192.200.0.101/32", "192.200.0.116/32"} {
		found := false
		for _, cidr := range got {
			if cidr == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("controlplane fallback CIDRs missing %s: %v", want, got)
		}
	}
}

func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[string]bool{}
	for _, v := range a {
		m[v] = true
	}
	for _, v := range b {
		if !m[v] {
			return false
		}
	}
	return true
}
