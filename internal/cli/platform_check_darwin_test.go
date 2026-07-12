//go:build darwin

package cli

import "testing"

func TestDarwinHasTailscaleOverlayRoute(t *testing.T) {
	routes := `
Destination        Gateway            Flags        Netif Expire
default            192.168.50.2       UGScg          en0
100.64/10          link#24            UCS          utun3
`
	if !darwinHasTailscaleOverlayRoute(routes) {
		t.Fatal("expected 100.64/10 to be detected as Tailscale overlay")
	}
}

func TestDarwinHasTailscaleOverlayRouteAbsent(t *testing.T) {
	routes := `
Destination        Gateway            Flags        Netif Expire
0/1                utun5              USc         utun5
128.0/1            utun5              USc         utun5
`
	if darwinHasTailscaleOverlayRoute(routes) {
		t.Fatal("split-default routes alone should not count as Tailscale overlay")
	}
}

func TestDarwinRouteGetInterface(t *testing.T) {
	out := `
   route to: 100.100.100.100
destination: default
  interface: utun5
`
	if got := darwinRouteGetInterface(out); got != "utun5" {
		t.Fatalf("interface = %q, want utun5", got)
	}
}

func TestDarwinHasZeroTierInterface(t *testing.T) {
	out := `
zt3jnm2k4a: flags=8843<UP,BROADCAST,RUNNING,SIMPLEX,MULTICAST> mtu 2800
	inet 10.147.17.21 netmask 0xffffff00 broadcast 10.147.17.255
`
	if !darwinHasZeroTierInterface(out) {
		t.Fatal("expected zt* interface to be detected as ZeroTier")
	}
}

func TestDarwinHasZeroTierInterfaceByDescription(t *testing.T) {
	out := `
feth1234: flags=8843<UP,BROADCAST,RUNNING,SIMPLEX,MULTICAST> mtu 2800
	status: active
	description: ZeroTier virtual interface
`
	if !darwinHasZeroTierInterface(out) {
		t.Fatal("expected ZeroTier description to be detected")
	}
}
