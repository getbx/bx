package route

import (
	"net/netip"
	"testing"
)

func TestCIDRSet(t *testing.T) {
	s, err := NewCIDRSet([]string{"1.2.0.0/16", "10.0.0.0/8"})
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]bool{
		"1.2.3.4":   true,
		"1.3.0.1":   false,
		"10.0.1.1": true,
		"8.8.8.8":   false,
	}
	for ip, want := range cases {
		got := s.Contains(netip.MustParseAddr(ip))
		if got != want {
			t.Errorf("Contains(%s)=%v want %v", ip, got, want)
		}
	}
}

func TestCIDRSetSkipsBadLines(t *testing.T) {
	s, err := NewCIDRSet([]string{"", "# comment", "1.2.0.0/16", "garbage"})
	if err != nil {
		t.Fatal(err)
	}
	if !s.Contains(netip.MustParseAddr("1.2.3.4")) {
		t.Fatal("valid CIDR should still match")
	}
}
