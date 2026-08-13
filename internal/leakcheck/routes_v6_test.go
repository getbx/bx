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
			{Destination: "::/0", Interface: "utun1", Flags: "UGcIg"},
			{Destination: "::/0", Interface: "utun2", Flags: "UGcIg"},
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
			{Destination: "::/1", Interface: "lo0", Flags: "UGRScg"},
			{Destination: "8000::/1", Interface: "lo0", Flags: "UGRSc"},
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
