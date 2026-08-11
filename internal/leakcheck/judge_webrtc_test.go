package leakcheck

import (
	"strings"
	"testing"
)

func webrtcFinding(t *testing.T, browser BrowserReport, local LocalFacts) Finding {
	t.Helper()
	for _, f := range Judge(fixedTime(), browser, local).Findings {
		if f.ID == FindingWebRTC {
			return f
		}
	}
	t.Fatalf("报告里没有 %s", FindingWebRTC)
	return Finding{}
}

func TestWebRTCRule(t *testing.T) {
	for _, tc := range []struct {
		name        string
		browser     BrowserReport
		want        Verdict
		wantMention string // summary 里必须出现的关键字符串
	}{
		{
			// 核心那条:srflx 与 HTTP 出口不是同一个地址 = WebRTC 走了别的路。
			name:        "srflx 与出口不同即为泄漏",
			browser:     BrowserReport{ExitV4: "5.6.7.8", SRFLX: []string{"1.2.3.4"}},
			want:        Bad,
			wantMention: "1.2.3.4",
		},
		{
			// 多个 srflx,只要有一个对不上就是泄漏 —— 一条泄漏通路就够了。
			name:        "多个 srflx 里有一个对不上",
			browser:     BrowserReport{ExitV4: "5.6.7.8", SRFLX: []string{"5.6.7.8", "1.2.3.4"}},
			want:        Bad,
			wantMention: "1.2.3.4",
		},
		{
			name:        "srflx 与出口一致",
			browser:     BrowserReport{ExitV4: "5.6.7.8", SRFLX: []string{"5.6.7.8"}},
			want:        OK,
			wantMention: "5.6.7.8",
		},
		{
			// STUN 被挡。**这不是 ok。**「没拿到 candidate」不等于「没有泄漏」。
			name:        "STUN 被挡",
			browser:     BrowserReport{ExitV4: "5.6.7.8", STUNErr: "ICE gathering failed"},
			want:        NotChecked,
			wantMention: "ICE gathering failed",
		},
		{
			// 拿到了 srflx,但 HTTP 出口没问出来 —— 另一半缺席,判不了。
			name:        "出口缺席",
			browser:     BrowserReport{SRFLX: []string{"1.2.3.4"}, ExitV4Err: "load failed"},
			want:        NotChecked,
			wantMention: "load failed",
		},
		{
			// 两半都没有。
			name:    "什么都没有",
			browser: BrowserReport{},
			want:    NotChecked,
		},
		{
			// srflx 是空列表但 STUN 没报错(ICE 收集完成、一个 srflx 都没有):
			// 常见于 UDP 被完全阻断。**仍然是 not checked** —— 我们没能观测到
			// WebRTC 的出口,不是观测到它没问题。
			name:    "ICE 完成但零 srflx",
			browser: BrowserReport{ExitV4: "5.6.7.8", SRFLX: nil},
			want:    NotChecked,
		},
		{
			// **回声端返回的不是地址。** `fetch` 对 4xx/5xx 不 reject,所以
			// Cloudflare 的拦截页、强制门户的登录页、429 的 body 都会原样变成
			// ExitV4 —— 而 bx 的用户在 GFW 后面,三个端点全是 Cloudflare 前置的,
			// 这不是奇景。
			//
			// 拿它跟 srflx 比必然不等,于是这条规则会**在一台完全正常的机器上**
			// 喊「WebRTC is bypassing the tunnel」。**乱喊狼来了与恒绿是同一条
			// 设计风险的两面**:两者都把这个界面训练成装饰。
			name: "v4 回声返回的是一张错误页",
			browser: BrowserReport{
				ExitV4: "<!DOCTYPE html><html>error 1015</html>",
				SRFLX:  []string{"203.0.113.9"},
			},
			want:        NotChecked,
			wantMention: "not an IP address",
		},
		{
			// 同一件事的另一种形状:强制门户返回一段人话。
			name:        "v4 回声返回一句人话",
			browser:     BrowserReport{ExitV4: "Access denied", SRFLX: []string{"203.0.113.9"}},
			want:        NotChecked,
			wantMention: "not an IP address",
		},
		{
			// 前后有空白的合法地址仍然要认(回声端普遍带一个换行,页面已 trim,
			// 但判据不该指望上游替它做归一化)。
			name:    "带空白的合法地址仍然判得了",
			browser: BrowserReport{ExitV4: " 5.6.7.8 ", SRFLX: []string{"5.6.7.8"}},
			want:    OK,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := webrtcFinding(t, tc.browser, LocalFacts{})
			if f.Verdict != tc.want {
				t.Fatalf("判定应为 %s,得到 %s(summary=%q)", tc.want, f.Verdict, f.Summary)
			}
			if tc.wantMention != "" && !strings.Contains(f.Summary+strings.Join(f.Evidence, " "), tc.wantMention) {
				t.Errorf("结论必须出示依据 %q,summary=%q evidence=%v", tc.wantMention, f.Summary, f.Evidence)
			}
		})
	}
}

// 引用第三方原文时必须截短。真实的 Cloudflare 拦截页是上千字节(上报体上限是
// 1MB),整段贴进 summary 会把结论本身冲出屏幕 —— 而 `bx leakcheck` 的输出是
// 终端里一行行读的,`--json` 那份还要给 agent 读。
func TestWebRTCQuotesAThirdPartyBodyBounded(t *testing.T) {
	body := "<!DOCTYPE html><html><head><title>Attention Required! | Cloudflare</title></head>" +
		"<body>" + strings.Repeat("Error 1015 Ray ID: 8f0000000000abcd You are being rate limited. ", 40) +
		"</body></html>"
	f := webrtcFinding(t, BrowserReport{ExitV4: body, SRFLX: []string{"203.0.113.9"}}, LocalFacts{})
	if f.Verdict != NotChecked {
		t.Fatalf("回声端返回拦截页时判定应为 not checked,得到 %s", f.Verdict)
	}
	if len(f.Summary) > 400 {
		t.Errorf("summary 长 %d 字节 —— 第三方原文没有被截短,结论被它冲掉了:%q",
			len(f.Summary), f.Summary)
	}
	if !strings.Contains(f.Summary, "truncated") {
		t.Errorf("截短了就要说截短了,否则用户以为回声端真的只返回了这么多:%q", f.Summary)
	}
	// 换行必须压平:多行原文会把「一条结论一行」的渲染撕开。
	if strings.Contains(strings.Join(append(f.Evidence, f.Summary), ""), "\n") {
		t.Errorf("引用的原文里还有换行:%q / %v", f.Summary, f.Evidence)
	}
}

// 每条结论都要能展开看到依据(设计诚实性规则)。判成 bad 时尤其:
// 用户得看得出 bx 是拿哪两个地址做的比较,才可能发现 bx 判错了。
func TestWebRTCBadFindingCarriesBothHalves(t *testing.T) {
	f := webrtcFinding(t, BrowserReport{ExitV4: "5.6.7.8", SRFLX: []string{"1.2.3.4"}}, LocalFacts{})
	joined := strings.Join(f.Evidence, "\n")
	if !strings.Contains(joined, "5.6.7.8") || !strings.Contains(joined, "1.2.3.4") {
		t.Fatalf("bad 结论的 evidence 必须同时含 HTTP 出口与 srflx,得到 %v", f.Evidence)
	}
}
