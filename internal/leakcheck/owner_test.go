package leakcheck

import "testing"

// 「谁在管这条路」是这个功能所有结论的挂靠点,所以它的四态必须逐个钉住。
//
// **判据取自「一个公网包实际从哪个接口离开」**(采集器问的是 route -n get 1.1.1.1),
// 不是 `default` 路由 —— bx 用 split-default(0/1 + 128/1),真正的 default 仍指着
// 物理网关,问 default 会在 bx 开着时报出物理网卡。别家 VPN 大多也用同一招。
func TestWhoOwnsTheRoute(t *testing.T) {
	for _, tc := range []struct {
		name  string
		local LocalFacts
		want  TunnelOwner
	}{
		{
			name: "出口接口就是 bx 的 TUN",
			local: LocalFacts{
				DefaultRouteV4: InterfaceRef{Name: "utun11"},
				BXTunInterface: "utun11",
			},
			want: OwnerBX,
		},
		{
			name: "出口接口是隧道类,但不是 bx 的 —— 用法二:拿 bx 检测别的 VPN",
			local: LocalFacts{
				DefaultRouteV4: InterfaceRef{Name: "utun4"},
				BXTunInterface: "utun11",
			},
			want: OwnerOther,
		},
		{
			name: "出口接口是隧道类,而 bx 根本没在跑",
			local: LocalFacts{
				DefaultRouteV4: InterfaceRef{Name: "utun4"},
			},
			want: OwnerOther,
		},
		{
			name:  "出口接口是物理网卡 —— 没有任何隧道",
			local: LocalFacts{DefaultRouteV4: InterfaceRef{Name: "en0"}},
			want:  OwnerNone,
		},
		{
			name:  "出口接口没问出来",
			local: LocalFacts{DefaultRouteV4: InterfaceRef{Err: "route lookup failed"}},
			want:  OwnerUnknown,
		},
		{
			name:  "压根没采集",
			local: LocalFacts{},
			want:  OwnerUnknown,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := WhoOwnsTheRoute(tc.local); got != tc.want {
				t.Errorf("WhoOwnsTheRoute = %v, want %v", got, tc.want)
			}
		})
	}
}

// **认不出的接口名必须落在 Unknown,绝不落在 None。**
//
// 两个方向的代价不对称。判成 OwnerNone(「你没有隧道」)而实际上有,是虚惊;
// 判成 OwnerOther / OwnerBX(「有隧道在管」)而实际上没有,是**把一台裸奔的机器
// 说成受保护** —— 与 ipify 那次同一类的、方向相反的谎。
//
// 而 OwnerNone 本身也不是白给的:它会让页面说出「你看到的就是你自己的地址」。
// 所以它只发给**认得出是物理接口**的名字,认不出的一律 Unknown。
func TestUnrecognisedInterfaceNamesNeverBecomeNoTunnel(t *testing.T) {
	for _, name := range []string{"", "wg-quick-mullvad", "NordLynx", "zt0", "weird9"} {
		got := WhoOwnsTheRoute(LocalFacts{DefaultRouteV4: InterfaceRef{Name: name}})
		if got == OwnerNone {
			t.Errorf("接口 %q 判成了 OwnerNone —— 认不出的名字不许支撑「你没有隧道」这句话", name)
		}
	}
}

// 反过来:认得出的隧道接口名要真的认得出。漏认一个的后果是把一条**别人的**隧道
// 说成「没有隧道」,而用法二整个建立在认得出别人的隧道之上。
func TestKnownTunnelInterfacesAreRecognised(t *testing.T) {
	for _, name := range []string{"utun4", "tun0", "tap0", "ppp0", "ipsec0", "wg0"} {
		if got := WhoOwnsTheRoute(LocalFacts{DefaultRouteV4: InterfaceRef{Name: name}}); got != OwnerOther {
			t.Errorf("接口 %q → %v,want OwnerOther —— 用法二靠它认出别人的隧道", name, got)
		}
	}
}

// 零值必须是 Unknown,与 Verdict / Tristate 同一条纪律:**没问出来是零值,
// 而不是某个具体答案**。
func TestTunnelOwnerZeroValueIsUnknown(t *testing.T) {
	var zero TunnelOwner
	if zero != OwnerUnknown {
		t.Fatalf("零值 = %v,必须是 OwnerUnknown", zero)
	}
}

// 「这条路上有没有隧道」在这个包里有两个问法:WhoOwnsTheRoute(四态,供 WebRTC
// 那条用)与 isTunnelPath(二值,供 IPv6 那条用)。**它们对同一台机器必须一致。**
//
// 两者本来各自带着一份隧道接口前缀表,已经并成 describe.go 的 IsTunnelInterface
// 那唯一一份。分叉真正的害处是:同一台机器上 WebRTC 那条说「有隧道」而 IPv6 那条
// 说「没有」,两条结论当着用户的面自相矛盾。
//
// **这条守卫的边界必须说清楚,否则会被当成一个它给不了的保证。** 今天 isTunnelPath
// 是**调用** WhoOwnsTheRoute 实现的,所以两者按构造就不可能不一致 —— 改动判据
// (哪怕改错)时两边一起动,这条测试不会红。实测确认过:给 WhoOwnsTheRoute 多认
// 一种前缀,它照样绿。
//
// 它真正挡的是**另一件事**:有人把 isTunnelPath 重新实现成独立的一份,而那份漂了。
// 这也实测确认过 —— 换成一份自带前缀表、漏了 wg 又多认 zt 的实现,它立刻转红。
// 判据的正确性由各自的用例守,这条只守「不许再分叉」。
func TestBothTunnelQuestionsAgree(t *testing.T) {
	for _, name := range []string{
		"utun4", "utun11", "tun0", "tap0", "ppp0", "ipsec0", "wg0",
		"en0", "eth0", "wlan0", "bridge0", "lo0",
		"zt0", "NordLynx", "", "weird9",
	} {
		local := LocalFacts{DefaultRouteV4: InterfaceRef{Name: name}}
		owner := WhoOwnsTheRoute(local)
		wantTunnel := owner == OwnerBX || owner == OwnerOther
		if got := isTunnelPath(local); got != wantTunnel {
			t.Errorf("接口 %q:WhoOwnsTheRoute=%v(有隧道=%v)而 isTunnelPath=%v —— "+
				"两条规则会当着用户的面自相矛盾", name, owner, wantTunnel, got)
		}
	}
}
