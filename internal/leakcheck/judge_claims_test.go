package leakcheck

import (
	"strings"
	"testing"
)

// **分类按行为,不按产品名。**
//
// 同一个产品的分类会随配置翻转:Tailscale 开了 exit node 就抢默认路由(竞争者),
// 不开就只 claim 100.64/10(租户);WireGuard 的 AllowedIPs=10/8 是租户、
// =0.0.0.0/0 是竞争者。**产品名什么都没告诉你**,而新产品永远追不完,
// 进程名/接口名在 macOS 上又分不开(都叫 utunN)。
//
// 可观测的判据只有一个:**它在路由表里 claim 了公网空间没有。**
func TestTunnelClaimsAreJudgedByBehaviourNotByName(t *testing.T) {
	for _, tc := range []struct {
		name       string
		routes     []RouteEntry
		wantIface  string
		wantPublic bool
	}{
		{
			// Tailscale 不开 exit node:只要 CGNAT,不碰公网。
			name: "只 claim CGNAT —— 叠加,不抢",
			routes: []RouteEntry{
				{Destination: "0/1", Interface: "utun11", Flags: "UScg"},
				{Destination: "100.64/10", Interface: "utun4", Flags: "USc"},
			},
			wantIface: "utun4", wantPublic: false,
		},
		{
			// Tailscale 开了 exit node,或者一条全隧道 WireGuard —— 同一种行为。
			name: "claim 了 split-default 两半 —— 它在抢公网流量",
			routes: []RouteEntry{
				{Destination: "0/1", Interface: "utun11", Flags: "UScg"},
				{Destination: "0/1", Interface: "utun4", Flags: "USc"},
				{Destination: "128.0/1", Interface: "utun4", Flags: "USc"},
			},
			wantIface: "utun4", wantPublic: true,
		},
		{
			// 分割隧道的 WireGuard:只要一段私网。
			name: "只 claim 私网段 —— 叠加",
			routes: []RouteEntry{
				{Destination: "0/1", Interface: "utun11", Flags: "UScg"},
				{Destination: "10.8.0/24", Interface: "utun3", Flags: "USc"},
			},
			wantIface: "utun3", wantPublic: false,
		},
		{
			name: "claim 了一段公网 —— 在抢",
			routes: []RouteEntry{
				{Destination: "0/1", Interface: "utun11", Flags: "UScg"},
				{Destination: "104", Interface: "utun3", Flags: "USc"},
			},
			wantIface: "utun3", wantPublic: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			local := LocalFacts{
				DefaultRouteV4: InterfaceRef{Name: "utun11"},
				BXTunInterface: "utun11",
				Routes:         tc.routes,
			}
			claims := ClassifyTunnels(local)
			var found *TunnelClaim
			for i := range claims {
				if claims[i].Interface == tc.wantIface {
					found = &claims[i]
				}
			}
			if found == nil {
				t.Fatalf("没有认出 %s 的 claim,得到 %+v", tc.wantIface, claims)
			}
			if found.ClaimsPublicSpace != tc.wantPublic {
				t.Fatalf("%s ClaimsPublicSpace = %v, want %v(claim=%v)",
					tc.wantIface, found.ClaimsPublicSpace, tc.wantPublic, found.Prefixes)
			}
		})
	}
}

// **bx 自己不算在内。** 把自己的 split-default 报成「有人在抢公网流量」是最蠢的一种错。
func TestBXsOwnTunnelIsNeverClassifiedAsACompetitor(t *testing.T) {
	local := LocalFacts{
		DefaultRouteV4: InterfaceRef{Name: "utun11"},
		BXTunInterface: "utun11",
		Routes: []RouteEntry{
			{Destination: "0/1", Interface: "utun11", Flags: "UScg"},
			{Destination: "128.0/1", Interface: "utun11", Flags: "USc"},
		},
	}
	for _, c := range ClassifyTunnels(local) {
		if c.Interface == "utun11" {
			t.Fatalf("把 bx 自己的隧道当成了别人:%+v", c)
		}
	}
}

// 物理网卡不是隧道 —— `default` 走 en0 是 split-default 的常态,不是有人在抢。
func TestPhysicalInterfacesAreNotTunnelClaims(t *testing.T) {
	local := LocalFacts{
		DefaultRouteV4: InterfaceRef{Name: "utun11"},
		BXTunInterface: "utun11",
		Routes: []RouteEntry{
			{Destination: "0/1", Interface: "utun11", Flags: "UScg"},
			{Destination: "104", Interface: "en0", Flags: "UGSc"},
		},
	}
	if got := ClassifyTunnels(local); len(got) != 0 {
		t.Fatalf("把物理网卡认成了隧道 claim:%+v", got)
	}
}

// **结论句里出现的必须是行为,产品名只作提示。**
//
// 「utun4 claim 了整个公网」是可观测的事实;「你可能开了 Tailscale exit node」
// 是给用户的线索。把后者当分类依据,就会在 WireGuard 全隧道那种情形下完全错过。
func TestWordingLeadsWithBehaviourAndOnlyHintsAtTheProduct(t *testing.T) {
	local := LocalFacts{
		DefaultRouteV4: InterfaceRef{Name: "utun4"},
		OverlayTenants: []string{"tailscale"},
		Routes: []RouteEntry{
			{Destination: "0/1", Interface: "utun4", Flags: "UScg"},
			{Destination: "128.0/1", Interface: "utun4", Flags: "USc"},
		},
	}
	f := judgeWebRTC(BrowserReport{ExitV4: "203.0.113.9", SRFLX: []string{"203.0.113.9"}}, local)
	joined := strings.ToLower(f.Summary + " " + strings.Join(f.Evidence, " "))
	if !strings.Contains(joined, "0.0.0.0/1") && !strings.Contains(joined, "all public") {
		t.Errorf("没说出可观测的行为(它 claim 了什么):%q", joined)
	}
	if !strings.Contains(joined, "tailscale") {
		t.Errorf("产品线索也该给:%q", joined)
	}
}
