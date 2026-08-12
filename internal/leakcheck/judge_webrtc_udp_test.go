package leakcheck

import (
	"strings"
	"testing"
)

func bxOwnedWithUDP(mode, transport string) LocalFacts {
	f := tunnelOwnedByBX()
	f.BXUDPMode = mode
	f.BXUDPTransport = transport
	return f
}

// **bx 自己的按类分流会被这条规则误报成泄漏。**
//
// `udp.transport: hysteria2://另一台服务器` 时,UDP 经隧道出去、只是出口是另一台,
// 于是 srflx ≠ HTTP 出口 —— 零泄漏,而规则喊「WebRTC is bypassing the tunnel」。
//
// **修法刻意不是判 ok。** bx 不知道那台 UDP 服务器的出口地址长什么样,所以它
// 分不清「srflx 是 hysteria 的出口」与「srflx 是用户真实 IP」。判 ok 就是把一个
// 假阳性换成假阴性 —— 而后者正是这个功能存在的理由。诚实的答案是「查不了」,
// 并且说清楚为什么、以及怎么才能得到确定答案。
func TestWebRTCDoesNotCryLeakForBXsOwnUDPTransport(t *testing.T) {
	browser := BrowserReport{ExitV4: "203.0.113.9", SRFLX: []string{"198.51.100.7"}}
	f := judgeWebRTC(browser, bxOwnedWithUDP("proxy", "hysteria2://pw@198.51.100.7:8443"))

	if f.Verdict == Bad {
		t.Fatalf("判成了泄漏,而 UDP 本来就配了单独的隧道:%q", f.Verdict)
	}
	if f.Verdict == OK {
		t.Fatalf("判成了 ok —— bx 分不清「hysteria 的出口」与「用户真实 IP」," +
			"判 ok 是把假阳性换成假阴性")
	}
	joined := f.Summary + strings.Join(f.Evidence, " ")
	if !strings.Contains(joined, "udp.transport") {
		t.Errorf("必须点名是哪一条配置造成的,否则用户无从判断:%q", f.Summary)
	}
}

// direct-realtime 那一档**仍然判 bad,而且必须** —— UDP 真的以真实 IP 直连,
// 这正是泄漏。只是措辞要说清楚这是配置选的,不是故障。
func TestWebRTCStillReportsDirectRealtimeAsALeak(t *testing.T) {
	browser := BrowserReport{ExitV4: "203.0.113.9", SRFLX: []string{"198.51.100.7"}}
	f := judgeWebRTC(browser, bxOwnedWithUDP("direct-realtime", ""))

	if f.Verdict != Bad {
		t.Fatalf("判定 = %v,want Bad —— direct-realtime 就是以真实 IP 直连 UDP", f.Verdict)
	}
	if !strings.Contains(f.Summary+strings.Join(f.Evidence, " "), "udp.mode") {
		t.Errorf("必须点名是哪一条配置,用户才知道去关哪个开关:%q", f.Summary)
	}
}

// **没有任何 UDP 特殊配置时,原来那条指控一个字不许软化。** 少了这一条,
// 上面两条可以靠「永远不判 bad」来满足,而那等于把这条规则整个废掉。
func TestWebRTCKeepsAccusingWhenNoUDPSplitExplainsIt(t *testing.T) {
	browser := BrowserReport{ExitV4: "203.0.113.9", SRFLX: []string{"198.51.100.7"}}
	for _, local := range []LocalFacts{
		bxOwnedWithUDP("proxy", ""),
		bxOwnedWithUDP("", ""),
	} {
		f := judgeWebRTC(browser, local)
		if f.Verdict != Bad {
			t.Errorf("udp_mode=%q transport=%q 时判定 = %v,want Bad",
				local.BXUDPMode, local.BXUDPTransport, f.Verdict)
		}
	}
}

// **别人的隧道不吃这条豁免。** BXUDPTransport 描述的是 bx 自己的配置;当路由归
// 别人的 VPN 管时,bx 那份配置与眼前这条隧道无关,拿它去解释一个不一致就是
// 张冠李戴 —— 而代价是把别人 VPN 的真实泄漏说成「查不了」。
func TestWebRTCUDPExemptionDoesNotApplyToSomebodyElsesTunnel(t *testing.T) {
	local := LocalFacts{
		DefaultRouteV4: InterfaceRef{Name: "utun4"},
		BXUDPTransport: "hysteria2://pw@198.51.100.7:8443",
	}
	f := judgeWebRTC(BrowserReport{ExitV4: "203.0.113.9", SRFLX: []string{"198.51.100.7"}}, local)
	if f.Verdict != Bad {
		t.Fatalf("判定 = %v,want Bad —— bx 的 UDP 配置解释不了别人的隧道", f.Verdict)
	}
}
