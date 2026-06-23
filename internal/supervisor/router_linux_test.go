//go:build linux

package supervisor

import "testing"

func TestParseNftHandles(t *testing.T) {
	out := `table inet fw4 {
	chain forward {
		ct state established,related accept # handle 7
		iifname "br-lan" meta nfproto ipv6 drop comment "bxr" # handle 41
		iifname "br-lan" oifname "bx0" accept comment "bxr" # handle 42
		iifname "br-lan" oifname "wan" accept comment "other" # handle 9
	}
}`
	hs := parseNftHandles(out, "bxr")
	if len(hs) != 2 {
		t.Fatalf("want 2 handles, got %v", hs)
	}
	if hs[0] != 41 || hs[1] != 42 {
		t.Fatalf("want [41 42], got %v", hs)
	}
}

func TestParseNftHandlesIgnoresOtherComments(t *testing.T) {
	out := `iifname "br-lan" oifname "wan" accept comment "other" # handle 9`
	if hs := parseNftHandles(out, "bxr"); len(hs) != 0 {
		t.Fatalf("want none, got %v", hs)
	}
}

func TestParseNftHandlesEmpty(t *testing.T) {
	if hs := parseNftHandles("", "bxr"); len(hs) != 0 {
		t.Fatalf("want none, got %v", hs)
	}
}
