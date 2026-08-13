package leakcheck

import (
	"strings"
	"testing"
	"time"
)

// 两条路出口一致时的 ok,**前提是先有一条隧道可绕**。
//
// 这是发布代码里的一个真问题:什么隧道都没开的时候,srflx == exit == 用户自己的
// ISP 地址,judgeWebRTC 照样打绿勾「WebRTC and HTTP both left from X」。技术上没
// 撒谎,读起来是「我很安全」—— 而在「bx 关着、拿它检测别的 VPN」这个用法里,
// 这是方向相反的错误:**一台没有任何保护的机器被报成正常**,与 ipify 那次同一类。
//
// 规则刻意是**单边**的:不知道有没有隧道就不许判 ok,但仍然可以判 bad ——
// 两条路出口不一致本身就是观测到的事实,不依赖隧道是否存在。
func TestWebRTCNeverGreenWithoutAKnownTunnel(t *testing.T) {
	const addr = "203.0.113.9"
	for _, tc := range []struct {
		name  string
		local LocalFacts
	}{
		{"没有任何隧道", LocalFacts{DefaultRouteV4: InterfaceRef{Name: "en0"}}},
		{"归谁管没问出来", LocalFacts{DefaultRouteV4: InterfaceRef{Err: "route lookup failed"}}},
		{"接口名认不出来", LocalFacts{DefaultRouteV4: InterfaceRef{Name: "zt0"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := judgeWebRTC(BrowserReport{ExitV4: addr, SRFLX: []string{addr}}, tc.local)
			if f.Verdict == OK {
				t.Fatalf("判成了 ok:%q —— 没有一条已知的隧道,「没有绕过隧道」不是好消息", f.Summary)
			}
			if !strings.Contains(f.Summary, addr) {
				t.Errorf("结论里没有出现看到的地址 %q,用户无从判断:%q", addr, f.Summary)
			}
		})
	}
}

// 反过来:**确实有一条隧道在管**的时候,一致就该是 ok。少了这一条,上面那条守卫
// 可以靠「永远不判 ok」来满足,而那等于把这条规则整个废掉。
func TestWebRTCStillGreenWhenATunnelActuallyOwnsTheRoute(t *testing.T) {
	const addr = "203.0.113.9"
	for _, tc := range []struct {
		name  string
		local LocalFacts
	}{
		{"bx 在管", LocalFacts{DefaultRouteV4: InterfaceRef{Name: "utun11"}, BXTunInterface: "utun11"}},
		{"别人的 VPN 在管", LocalFacts{DefaultRouteV4: InterfaceRef{Name: "utun4"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := judgeWebRTC(BrowserReport{ExitV4: addr, SRFLX: []string{addr}}, tc.local)
			if f.Verdict != OK {
				t.Fatalf("判成了 %v:%q —— 有隧道且两条路一致,这就是 ok", f.Verdict, f.Summary)
			}
		})
	}
}

// 不一致仍然要判 bad,**与归谁管无关**。规则是单边的:ok 需要前提,bad 不需要。
func TestWebRTCMismatchIsBadRegardlessOfWhoOwnsTheRoute(t *testing.T) {
	for _, local := range []LocalFacts{
		{DefaultRouteV4: InterfaceRef{Name: "en0"}},
		{DefaultRouteV4: InterfaceRef{Err: "boom"}},
		{DefaultRouteV4: InterfaceRef{Name: "utun4"}},
		{DefaultRouteV4: InterfaceRef{Name: "utun11"}, BXTunInterface: "utun11"},
	} {
		f := judgeWebRTC(BrowserReport{ExitV4: "203.0.113.9", SRFLX: []string{"198.51.100.7"}}, local)
		if f.Verdict != Bad {
			t.Errorf("owner=%v 时不一致判成了 %v:%q", WhoOwnsTheRoute(local), f.Verdict, f.Summary)
		}
	}
}

// **措辞不许冒认 bx 的功劳。** 别人的隧道在管时,结论不能说成 bx 干的 ——
// 「拿 bx 检测别的 VPN」这个用法里,用户要读到的是关于**他自己那条 VPN** 的判断。
//
// **判据钉的是「冒认」,不是「提到 bx」。** 第一版禁的是光秃秃的 `bx `,于是连一句
// 诚实的「bx is not running」都被禁掉 —— 而那正是 traffic_carrier 在这种局面下最该
// 说的话(bx 没在跑,所以别人的 VPN 拿着流量不是问题)。禁拼法不禁语义,是这个仓库
// 反复栽过的形状;这里禁的是「说 bx 在承载/在管这条流量」。
func TestWebRTCDoesNotCreditBXForSomebodyElsesTunnel(t *testing.T) {
	report := Judge(time.Now(),
		BrowserReport{ExitV4: "203.0.113.9", SRFLX: []string{"203.0.113.9"}},
		LocalFacts{DefaultRouteV4: InterfaceRef{Name: "utun4"}})

	for _, f := range report.Findings {
		lowered := strings.ToLower(f.Summary)
		for _, credit := range []string{
			"carried by bx", "bx is carrying", "bx is managing",
			"through bx", "tunnel bx is",
		} {
			if strings.Contains(lowered, credit) {
				t.Errorf("%s 把别人的隧道说成了 bx 的功劳(%q):%q", f.ID, credit, f.Summary)
			}
		}
	}
}
