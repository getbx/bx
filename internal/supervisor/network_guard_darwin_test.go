//go:build darwin

package supervisor

import "testing"

func TestDarwinGuardHasTailscaleOverlayRoute(t *testing.T) {
	routes := `
Destination        Gateway            Flags        Netif Expire
100.64/10          link#24            UCS          utun3
`
	if !darwinHasTailscaleOverlayRoute(routes) {
		t.Fatal("expected Tailscale overlay route")
	}
}

func TestDarwinGuardSystemProxyEnabled(t *testing.T) {
	out := `
<dictionary> {
  HTTPEnable : 0
  HTTPSEnable : 1
}
`
	if !darwinSystemProxyEnabled(out) {
		t.Fatal("expected HTTPS system proxy to be detected")
	}
}

func TestDarwinGuardConnectedNetworkService(t *testing.T) {
	out := `
Available network connection services in the current set (*=enabled):
* (Disconnected) Personal VPN
* (Connected) Work VPN [VPN:com.example.vpn]
`
	if got := darwinConnectedNetworkService(out); got != "Work VPN [VPN:com.example.vpn]" {
		t.Fatalf("connected service = %q", got)
	}
}
