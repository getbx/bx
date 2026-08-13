package leakcheck

import (
	"strings"
	"testing"
)

// **共存正常时 bx 应该闭嘴。**
//
// 用户对 VPN 与 overlay 的心理模型不同:两个 VPN 一起跑,他预期冲突;bx 加 Tailscale,
// 他预期「本来就该共存」。而技术上这个直觉是准的 —— VPN 要默认路由(排他),
// overlay 要一段地址 + 自己的命名空间(叠加)。
//
// 所以 bx 在共存正常时不该说话:一条「检测到 Tailscale」的提示对用户毫无用处,
// 只会让他以为出了事。
func TestNothingIsSaidAboutATenantThatIsBehaving(t *testing.T) {
	local := tunnelOwnedByBX()
	local.OverlayTenants = []string{"tailscale"}
	report := Judge(fixedTime(), BrowserReport{ExitV4: "203.0.113.9", SRFLX: []string{"203.0.113.9"}}, local)

	for _, f := range report.Findings {
		// **证据区也要查。** 第一版只查 Summary,而把这句话挪进 Evidence 的变异
		// 照样全绿 —— 用户一展开就看见了,「闭嘴」当然也管那里。
		text := f.Summary + " " + strings.Join(f.Evidence, " ")
		if strings.Contains(strings.ToLower(text), "tailscale") {
			t.Errorf("%s 在一切正常时提到了 tailscale:%q —— 共存是用户的默认预期,"+
				"正常时说话只会让他以为出了事", f.ID, text)
		}
	}
}

// **共存被打破时必须点名。**
//
// 用户最容易踩、而 bx 此前完全认不出的情形:Tailscale 开了 exit node。那一刻它要
// 默认路由,从「租户」变成「竞争者」—— 而用户仍然觉得「这俩本来就该共存」,
// 于是完全不知道发生了什么。
//
// bx 此前只会说「一条 utun4 上不是 bx 的隧道在管」,**不会把它和用户明知在跑的
// Tailscale 联系起来**。在 macOS 上两者都叫 utunN,靠接口名分不开 —— 但
// 「有租户在跑」+「默认路由不归 bx」这两件事合起来足以点名嫌疑,
// 而措辞必须是**怀疑而非断言**:那条隧道也可能是第三个 VPN。
func TestATenantThatTookTheDefaultRouteIsNamed(t *testing.T) {
	local := LocalFacts{
		DefaultRouteV4: InterfaceRef{Name: "utun4", Display: "Unidentified VPN (utun4)"},
		OverlayTenants: []string{"tailscale"},
	}
	f := judgeWebRTC(BrowserReport{ExitV4: "203.0.113.9", SRFLX: []string{"203.0.113.9"}}, local)

	joined := f.Summary + " " + strings.Join(f.Evidence, " ")
	if !strings.Contains(strings.ToLower(joined), "tailscale") {
		t.Fatalf("默认路由被别人拿走、而 tailscale 在跑,却没点名:%q", joined)
	}
	if !strings.Contains(strings.ToLower(joined), "exit node") {
		t.Errorf("没提 exit node —— 那是这个组合最常见的原因,而用户不会自己想到:%q", joined)
	}
	// **不许把怀疑说成断言。** 那条隧道也可能是第三个 VPN,而把它一口咬定成
	// Tailscale 会让用户去关一个根本没占路由的东西。
	lowered := strings.ToLower(joined)
	if strings.Contains(lowered, "tailscale is carrying") || strings.Contains(lowered, "tailscale took") {
		t.Errorf("把怀疑说成了断言:%q", joined)
	}
}

// 没有租户在跑时,措辞一个字都不该变 —— 那是既有行为,不该被这条改动带偏。
func TestWordingIsUnchangedWhenNoTenantIsRunning(t *testing.T) {
	local := LocalFacts{DefaultRouteV4: InterfaceRef{Name: "utun4"}}
	f := judgeWebRTC(BrowserReport{ExitV4: "203.0.113.9", SRFLX: []string{"203.0.113.9"}}, local)
	if strings.Contains(strings.ToLower(f.Summary+strings.Join(f.Evidence, " ")), "tailscale") {
		t.Errorf("没有租户在跑却提到了 tailscale:%q", f.Summary)
	}
}
