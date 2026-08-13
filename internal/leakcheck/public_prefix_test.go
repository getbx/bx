package leakcheck

import (
	"net/netip"
	"testing"
)

// **`0.0.0.0/2` 是公网空间,而它差点被静默放过。**
//
// 判据里的 `addr.IsUnspecified()` 本意是排除 `0.0.0.0` 这个**主机地址**,但它作用在
// 前缀的网络地址上 —— 于是 0.0.0.0/1、0.0.0.0/2、0.0.0.0/3 全部被否掉,而它们覆盖的
// 是 1.0.0.0/8、8.8.8.8 这些实打实的公网。
//
// 后果分两处,都不小:
//   - TunnelVision 那条(routeEscapesTunnel):攻击者推一条 `0.0.0.0/2` 把四分之一的
//     互联网从物理网卡送走,**而检查一声不吭** —— 那正是这条规则唯一要抓的东西。
//   - 隧道分类(ClassifyTunnels):只 claim `0.0.0.0/1` 的全隧道 VPN 不算竞争者。
//
// 这个洞是变异测试没转红时查出来的,不是读代码读出来的。
func TestZeroPrefixedRangesAreStillPublic(t *testing.T) {
	for _, tc := range []struct {
		cidr string
		want bool
	}{
		{"0.0.0.0/1", true}, // 覆盖 0–127.255.255.255,含大量公网
		{"0.0.0.0/2", true}, // 攻击最爱用的宽度
		{"0.0.0.0/3", true},
		{"128.0.0.0/1", true},
		{"104.0.0.0/8", true},
		// 「本网络」RFC1122:0.0.0.0/8 整段确实不是可路由的公网目的地。
		{"0.0.0.0/8", false},
		{"0.0.0.0/32", false},
		{"10.0.0.0/8", false},
		{"192.168.0.0/16", false},
		{"100.64.0.0/10", false},
		{"127.0.0.0/8", false},
		{"169.254.0.0/16", false},
		{"224.0.0.0/4", false},
	} {
		t.Run(tc.cidr, func(t *testing.T) {
			if got := isPublicPrefix(netip.MustParsePrefix(tc.cidr)); got != tc.want {
				t.Fatalf("isPublicPrefix(%s) = %v, want %v", tc.cidr, got, tc.want)
			}
		})
	}
}

// TunnelVision 那条必须抓得住 `0.0.0.0/2` —— **这正是它唯一要抓的形状**。
func TestRouteEscapeCatchesZeroPrefixedAttackRanges(t *testing.T) {
	local := withRoutes(
		RouteEntry{Destination: "0/1", Interface: "utun11", Flags: "UScg"},
		RouteEntry{Destination: "0.0.0.0/2", Interface: "en0", Flags: "UGSc"},
	)
	f := identityFinding(t, FindingRouteEscape, BrowserReport{ExitV4: "203.0.113.9"}, local)
	if f.Verdict != Bad {
		t.Fatalf("0.0.0.0/2 从物理网卡逃逸没被抓到:%v %q", f.Verdict, f.Summary)
	}
}

// 隧道分类同理:只 claim `0.0.0.0/1` 的全隧道 VPN 也是竞争者。
func TestTunnelClaimingOnlyLowerHalfIsStillCompeting(t *testing.T) {
	local := LocalFacts{
		DefaultRouteV4: InterfaceRef{Name: "utun11"},
		BXTunInterface: "utun11",
		Routes: []RouteEntry{
			{Destination: "0/1", Interface: "utun11", Flags: "UScg"},
			{Destination: "0/1", Interface: "utun4", Flags: "USc"},
		},
	}
	claims := ClassifyTunnels(local)
	if len(claims) != 1 || !claims[0].ClaimsPublicSpace {
		t.Fatalf("只 claim 0.0.0.0/1 的隧道没被认成竞争者:%+v", claims)
	}
}
