package leakcheck

import (
	"strings"
	"testing"
)

// netstat 的目的地是**简写**的:省掉末尾的零字节,自然前缀长度也省掉。
// `10` 是 10.0.0.0/8,`172.16/12` 要补足四段,`0/1` 是 split-default 的一半。
// 认错一个就会把私网段当成公网段报出来,或者反过来漏掉一条真的逃逸路由。
func TestParseRouteDestination(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
		bad  bool
	}{
		{in: "10", want: "10.0.0.0/8"},
		{in: "127", want: "127.0.0.0/8"},
		{in: "169.254", want: "169.254.0.0/16"},
		{in: "172.16/12", want: "172.16.0.0/12"},
		{in: "10.84.6/23", want: "10.84.6.0/23"},
		{in: "0/1", want: "0.0.0.0/1"},
		{in: "128.0/1", want: "128.0.0.0/1"},
		{in: "45.159.97.61/32", want: "45.159.97.61/32"},
		{in: "166.1.190.123/32", want: "166.1.190.123/32"},
		{in: "default", bad: true},
		{in: "", bad: true},
		{in: "not-an-address", bad: true},
	} {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseRouteDestination(tc.in)
			if tc.bad {
				if err == nil {
					t.Fatalf("%q 应当解析失败,得到 %v", tc.in, got)
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

func withRoutes(entries ...RouteEntry) LocalFacts {
	f := tunnelOwnedByBX()
	f.Routes = entries
	return f
}

// TunnelVision(CVE-2024-3661)与 TunnelCrack 的 ServerIP 是同一个形状:
// 往路由表里塞**比隧道的 split-default 更具体**的路由,把流量从物理网卡送走。
// bx 是路由型代理,这就是它的攻击面 —— 而 bx 自己的屏障用 /2 压 /1,用的正是同一招。
//
// **判据的形状由真机决定,不是拍脑袋。** 本机(2026-08-11)路由表里有 108 条公网
// /32 经物理网关 —— 那是 bx 自己的 server bypass(其中就有 VPS 的 166.1.190.123)。
// 把它们报成异常,这条检查在每一台正常工作的机器上都会红,于是被训练成噪声。
//
// 所以:**单主机(/32)是隧道旁路自己服务器的正常形状,不报;能捕获成片流量的
// 更宽前缀才报** —— 后者恰恰是攻击要的,因为攻击的目的是截走流量而不是一台主机。
func TestRouteEscapeRule(t *testing.T) {
	for _, tc := range []struct {
		name        string
		local       LocalFacts
		want        Verdict
		wantMention string
	}{
		{
			name: "真机形状:一堆 /32 经物理网关 —— 那是隧道自己的服务器旁路",
			local: withRoutes(
				RouteEntry{Destination: "0/1", Interface: "utun11", Flags: "UScg"},
				RouteEntry{Destination: "128.0/1", Interface: "utun11", Flags: "USc"},
				RouteEntry{Destination: "166.1.190.123/32", Interface: "en0", Flags: "UGSc"},
				RouteEntry{Destination: "45.159.97.61/32", Interface: "en0", Flags: "UGSc"},
				RouteEntry{Destination: "10", Interface: "en0", Flags: "UGSc"},
				RouteEntry{Destination: "172.16/12", Interface: "en0", Flags: "UGSc"},
				RouteEntry{Destination: "169.254", Interface: "en0", Flags: "UCS"},
			),
			want: OK,
		},
		{
			// 攻击的形状:一条 /2 压过 split-default 的 /1,把四分之一的互联网
			// 从物理网卡送走。
			name: "更宽的公网前缀经物理网卡 —— 这是 TunnelVision 的形状",
			local: withRoutes(
				RouteEntry{Destination: "0/1", Interface: "utun11", Flags: "UScg"},
				RouteEntry{Destination: "64.0/2", Interface: "en0", Flags: "UGSc"},
			),
			want:        Bad,
			wantMention: "64.0.0.0/2",
		},
		{
			name: "一条 /8 同样是逃逸",
			local: withRoutes(
				RouteEntry{Destination: "0/1", Interface: "utun11", Flags: "UScg"},
				RouteEntry{Destination: "104", Interface: "en0", Flags: "UGSc"},
			),
			want:        Bad,
			wantMention: "104.0.0.0/8",
		},
		{
			// bx 自己的屏障就是 /2 的 reject 路由。它们**阻断**流量而不是把流量
			// 送出去 —— 把自家的 fail-closed 屏障报成逃逸,是这条检查能犯的最蠢的错。
			name: "reject / blackhole 路由不是逃逸,它们是在挡",
			local: withRoutes(
				RouteEntry{Destination: "0/1", Interface: "utun11", Flags: "UScg"},
				RouteEntry{Destination: "64.0/2", Interface: "lo0", Flags: "UGSRc"},
				RouteEntry{Destination: "192.0/2", Interface: "lo0", Flags: "UGSBc"},
			),
			want: OK,
		},
		{
			name: "经隧道接口的宽前缀当然不是逃逸",
			local: withRoutes(
				RouteEntry{Destination: "0/1", Interface: "utun11", Flags: "UScg"},
				RouteEntry{Destination: "64.0/2", Interface: "utun11", Flags: "USc"},
			),
			want: OK,
		},
		{
			// default 本来就在物理网卡上(split-default 靠两条 /1 压过它),
			// 把它报成逃逸会让这条检查在每台机器上恒红。
			name: "default 经物理网卡是 split-default 的常态",
			local: withRoutes(
				RouteEntry{Destination: "0/1", Interface: "utun11", Flags: "UScg"},
				RouteEntry{Destination: "128.0/1", Interface: "utun11", Flags: "USc"},
			),
			want: OK,
		},
		{
			name:  "没读到路由表",
			local: func() LocalFacts { f := tunnelOwnedByBX(); f.RoutesErr = "netstat failed"; return f }(),
			want:  NotChecked, wantMention: "netstat failed",
		},
		{
			// 没有隧道就没有「绕过隧道」这回事。判 ok 会读成「你很安全」。
			name:  "没有隧道时这一项无从谈起",
			local: LocalFacts{DefaultRouteV4: InterfaceRef{Name: "en0"}, Routes: []RouteEntry{{Destination: "64.0/2", Interface: "en0", Flags: "UGSc"}}},
			want:  NotChecked,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := identityFinding(t, FindingRouteEscape, BrowserReport{ExitV4: "203.0.113.9"}, tc.local)
			if f.Verdict != tc.want {
				t.Fatalf("判定应为 %s,得到 %s(summary=%q)", tc.want, f.Verdict, f.Summary)
			}
			if tc.wantMention != "" &&
				!strings.Contains(f.Summary+strings.Join(f.Evidence, " "), tc.wantMention) {
				t.Errorf("结论必须出示依据 %q,summary=%q evidence=%v", tc.wantMention, f.Summary, f.Evidence)
			}
		})
	}
}

// **这一项不吃浏览器。** 它是纯本机路由表观测,一个包都不用发 —— 浏览器没跑过
// 也照样答得了,而把它压成 not checked 会白白丢掉一条真的能查出攻击的检查。
func TestRouteEscapeNeedsNothingFromTheBrowser(t *testing.T) {
	f := identityFinding(t, FindingRouteEscape, BrowserReport{}, withRoutes(
		RouteEntry{Destination: "0/1", Interface: "utun11", Flags: "UScg"},
		RouteEntry{Destination: "64.0/2", Interface: "en0", Flags: "UGSc"},
	))
	if f.Verdict != Bad {
		t.Fatalf("判定 = %v,want Bad —— 这一项不需要浏览器", f.Verdict)
	}
}
