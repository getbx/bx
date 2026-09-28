//go:build darwin

package platformcheck

import (
	"strings"
	"testing"
)

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

func TestDarwinSystemProxyEnabled(t *testing.T) {
	out := `
<dictionary> {
  HTTPEnable : 1
  HTTPPort : 7890
  HTTPProxy : 127.0.0.1
  SOCKSEnable : 0
}
`
	if !darwinSystemProxyEnabled(out) {
		t.Fatal("expected enabled HTTP proxy to be detected")
	}
}

func TestDarwinSystemProxyEnabledAbsent(t *testing.T) {
	out := `
<dictionary> {
  HTTPEnable : 0
  SOCKSEnable : 0
}
`
	if darwinSystemProxyEnabled(out) {
		t.Fatal("disabled proxy should not be detected as enabled")
	}
}

func TestDarwinConnectedNetworkService(t *testing.T) {
	out := `
Available network connection services in the current set (*=enabled):
* (Disconnected) Personal VPN
* (Connected) Work VPN [VPN:com.example.vpn]
`
	if got := darwinConnectedNetworkService(out); got != "Work VPN [VPN:com.example.vpn]" {
		t.Fatalf("connected service = %q", got)
	}
}

func TestDarwinConnectedNetworkServiceConnecting(t *testing.T) {
	out := `
Available network connection services in the current set (*=enabled):
* (Connecting) Cloudflare WARP [VPN:com.cloudflare.warp]
`
	if got := darwinConnectedNetworkService(out); got != "Cloudflare WARP [VPN:com.cloudflare.warp]" {
		t.Fatalf("connecting service = %q", got)
	}
}

func TestDarwinConnectedNetworkServiceAbsent(t *testing.T) {
	out := `
Available network connection services in the current set (*=enabled):
* (Disconnected) Work VPN
`
	if got := darwinConnectedNetworkService(out); got != "" {
		t.Fatalf("connected service = %q, want empty", got)
	}
}

// bx 的一次性 pf 重置(internal/pfreset)只该存在几秒;Core 崩溃留下的 anchor 或引用
// token 会把物理网卡上非 root 的公网 TCP/UDP 全拒掉,用户读到的是「网坏了」。
// 判定纯函数:anchor 里有规则或 token 文件在 ⇒ warn 并指向 bx down(它的强制拆除会冲);
// 都没有 ⇒ ok。
func TestPFResetResidueCheckReadsTheAnchorAndTheToken(t *testing.T) {
	c := darwinPFResetResidueCheck("block return-rst out quick on en0 ...\n", false)
	if c.Name != "pf_reset_residue" || c.Status != "warn" || !strings.Contains(c.Hint, "bx down") {
		t.Fatalf("leftover rules must warn and point at bx down, got %+v", c)
	}
	if c := darwinPFResetResidueCheck("", true); c.Status != "warn" || !strings.Contains(c.Detail, "token") {
		t.Fatalf("a leftover token must warn and say so, got %+v", c)
	}
	if c := darwinPFResetResidueCheck("", false); c.Status != "ok" {
		t.Fatalf("clean must be ok, got %+v", c)
	}
}
