package leakcheck

import (
	"net/netip"
	"testing"
)

// v6 目的地的形状与 v4 完全不同:有 `::` 缩写、有 `%zone` 后缀。
// 认不出就等于整个 v6 路由分析不存在 —— 而 TunnelVision 有 v6 版本(RA / DHCPv6 注入)。
func TestParseRouteDestinationHandlesIPv6(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
		bad  bool
	}{
		{in: "::/1", want: "::/1"},
		{in: "8000::/1", want: "8000::/1"},
		{in: "::1", want: "::1/128"},
		{in: "2001:db8::/32", want: "2001:db8::/32"},
		// **zone 后缀必须剥掉**,否则整条 fe80 路由解析失败,
		// 而链路本地是 v6 表里最常见的一类。
		{in: "fe80::%lo0/64", want: "fe80::/64"},
		{in: "fe80::1%lo0", want: "fe80::1/128"},
		{in: "fe80::%en0/64", want: "fe80::/64"},
		{in: "0.0.0.0/0", want: "0.0.0.0/0"},
		{in: "::/0", want: "::/0"},
		{in: "not-an-address", bad: true},
	} {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseRouteDestination(tc.in)
			if tc.bad {
				if err == nil {
					t.Fatalf("%q 应解析失败,得到 %v", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("%q: %v", tc.in, err)
			}
			if got.String() != tc.want {
				t.Fatalf("%q → %s, want %s", tc.in, got, tc.want)
			}
		})
	}
}

// v6 的公网是 2000::/3(全球单播);其余(链路本地 / ULA / 组播 / loopback)都不是。
func TestIsPublicPrefixHandlesIPv6(t *testing.T) {
	for _, tc := range []struct {
		cidr string
		want bool
	}{
		{"2000::/3", true},
		{"2001:db8::/32", true},
		{"::/0", true}, // 覆盖全球单播,是抢流量的形状
		{"::/1", true}, // 同上
		// **`8000::/1` 里没有任何已分配的全球单播。** 它覆盖 8000–ffff,那里是保留段、
		// ULA(fc00::/7)、链路本地(fe80::/10)、组播(ff00::/8);全球单播 2000::/3
		// 连同保留的 4000::/3、6000::/3 全在 `::/1` 那一半。
		//
		// 所以「抢一切」的那对 v6 路由是**不对称**的:只有下半段真的在抢可路由空间。
		// 分类不受影响 —— 装那一对的隧道会因为 `::/1` 被认出来。
		{"8000::/1", false},
		{"fe80::/10", false}, // 链路本地
		{"fc00::/7", false},  // ULA
		{"ff00::/8", false},  // 组播
		{"::1/128", false},   // loopback
	} {
		t.Run(tc.cidr, func(t *testing.T) {
			if got := isPublicPrefix(netip.MustParsePrefix(tc.cidr)); got != tc.want {
				t.Fatalf("isPublicPrefix(%s) = %v, want %v", tc.cidr, got, tc.want)
			}
		})
	}
}

// **接口作用域路由(RTF_IFSCOPE,flags 里的 I)不算 claim。**
//
// 真机(macOS)的 v6 表里有六条 `default via fe80::%utunN`,分别挂在 utun1..utun6 上,
// flags 都带 I —— 那是系统自己的作用域路由,只对已经绑定到该接口的流量生效,
// **不参与一般流量的竞争**。不排除它们,一台完全正常的 Mac 上会冒出六条
// 「另一条隧道抢走了公网空间」。
func TestInterfaceScopedRoutesAreNotClaims(t *testing.T) {
	local := LocalFacts{
		BXTunInterface: "utun0",
		Routes: []RouteEntry{
			{Destination: "::/0", Interface: "utun1", Flags: "UGcIg", Scoped: true},
			{Destination: "::/0", Interface: "utun2", Flags: "UGcIg", Scoped: true},
		},
	}
	if got := ClassifyTunnels(local); len(got) != 0 {
		t.Fatalf("作用域路由被当成了 claim:%+v —— 一台正常的 Mac 会冒出一堆误报", got)
	}
}

// bx 自己的 v6 屏障是 reject 路由(flags 带 R),不是 claim —— 它把流量扔掉,不是收走。
func TestBXsIPv6BarrierIsNotAClaim(t *testing.T) {
	local := LocalFacts{
		BXTunInterface: "utun0",
		Routes: []RouteEntry{
			{Destination: "::/1", Interface: "lo0", Flags: "UGRScg", Blocking: true},
			{Destination: "8000::/1", Interface: "lo0", Flags: "UGRSc", Blocking: true},
		},
	}
	if got := ClassifyTunnels(local); len(got) != 0 {
		t.Fatalf("把 bx 自己的 v6 屏障当成了别人的 claim:%+v", got)
	}
}

// **一条抢走全部 v6 流量的隧道必须被认出来。** 这是本轮要补的洞:
// 此前整套路由分析是 v4-only 的,v6 competitor 一个字都不会说。
func TestATunnelTakingAllIPv6IsDetected(t *testing.T) {
	local := LocalFacts{
		BXTunInterface: "utun0",
		Routes: []RouteEntry{
			{Destination: "::/0", Interface: "utun4", Flags: "UGSc"},
		},
	}
	claims := ClassifyTunnels(local)
	if len(claims) != 1 || !claims[0].ClaimsPublicSpace {
		t.Fatalf("抢走全部 v6 的隧道没被认出来:%+v", claims)
	}
}

// **v6 的逃逸路由是 /48、/56、/64 —— 而它们此前全被跳过。**
//
// routeEscapesTunnel 的上界 `Bits() >= 32` 是照 v4 写的(v4 的 /32 是单主机)。
// v6 收进来之后,任何比 /31 更具体的目的地都在到达 isPublicPrefix 之前就被丢掉,
// 而 RA / DHCPv6 注入的正是这个宽度 —— 也就是说「补上 v6 覆盖」那次改动,
// 对 TunnelVision 的 v6 形态**一条都没抓到**。审查抓到的。
func TestIPv6EscapeRoutesAreNotSkippedByTheIPv4HostCutoff(t *testing.T) {
	for _, dest := range []string{"2001:db8::/48", "2001:db8::/56", "2001:db8:1::/64"} {
		local := LocalFacts{
			DefaultRouteV4: InterfaceRef{Name: "utun11"},
			BXTunInterface: "utun11",
			InterfaceKinds: map[string]InterfaceKind{"utun11": InterfaceTunnel, "en0": InterfacePhysical},
			Routes: []RouteEntry{
				{Destination: "0/1", Interface: "utun11"},
				{Destination: dest, Interface: "en0"},
			},
		}
		f := identityFinding(t, FindingRouteEscape, BrowserReport{ExitV4: "203.0.113.9"}, local)
		if f.Verdict != Bad {
			t.Errorf("%s 经物理网卡逃逸没被抓到:%v %q", dest, f.Verdict, f.Summary)
		}
	}
	// v6 的单地址(/128)仍然是「单主机」,与 v4 的 /32 同类 —— 不报。
	local := LocalFacts{
		DefaultRouteV4: InterfaceRef{Name: "utun11"},
		BXTunInterface: "utun11",
		InterfaceKinds: map[string]InterfaceKind{"utun11": InterfaceTunnel, "en0": InterfacePhysical},
		Routes: []RouteEntry{
			{Destination: "0/1", Interface: "utun11"},
			{Destination: "2001:db8::1/128", Interface: "en0"},
		},
	}
	if f := identityFinding(t, FindingRouteEscape, BrowserReport{ExitV4: "203.0.113.9"}, local); f.Verdict != OK {
		t.Errorf("v6 单地址旁路被报成逃逸:%v %q", f.Verdict, f.Summary)
	}
}

// **直连子网不是「有人注入了路由」。**
//
// 一台 LAN 上带公网网段的 VPS/路由器,主表里有
// `203.0.113.0/24 dev eth0 proto kernel scope link` —— 24 位、非阻断、非作用域、
// 物理接口、公网前缀,于是被报成 CVE-2024-3661。**那会在每一台这样的机器上恒红**,
// 而 VPS / 路由器 / NAS 正是 bx 明确支持的部署形态。
//
// 判据:on-link(没有网关)的路由描述的是「这段就挂在这个接口上」,不是「发往这里
// 的包交给某个下一跳」——注入攻击要的是后者。
func TestOnLinkPublicSubnetsAreNotReportedAsInjectedRoutes(t *testing.T) {
	local := LocalFacts{
		DefaultRouteV4: InterfaceRef{Name: "utun11"},
		BXTunInterface: "utun11",
		InterfaceKinds: map[string]InterfaceKind{"utun11": InterfaceTunnel, "eth0": InterfacePhysical},
		Routes: []RouteEntry{
			{Destination: "0/1", Interface: "utun11"},
			{Destination: "203.0.113.0/24", Interface: "eth0", OnLink: true},
		},
	}
	f := identityFinding(t, FindingRouteEscape, BrowserReport{ExitV4: "203.0.113.9"}, local)
	if f.Verdict != OK {
		t.Fatalf("直连的公网子网被报成注入路由:%v %q —— 每台这样的 VPS 都会恒红", f.Verdict, f.Summary)
	}
	// 反面:同一个网段**带网关**就是真的逃逸(那正是 DHCP option 121 的形状)。
	local.Routes[1] = RouteEntry{Destination: "203.0.113.0/24", Interface: "eth0"}
	if g := identityFinding(t, FindingRouteEscape, BrowserReport{ExitV4: "203.0.113.9"}, local); g.Verdict != Bad {
		t.Fatalf("经网关的公网网段没被抓到:%v %q", g.Verdict, g.Summary)
	}
}
