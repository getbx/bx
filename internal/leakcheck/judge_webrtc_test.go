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

// 每条结论都要能展开看到依据(设计诚实性规则)。判成 bad 时尤其:
// 用户得看得出 bx 是拿哪两个地址做的比较,才可能发现 bx 判错了。
func TestWebRTCBadFindingCarriesBothHalves(t *testing.T) {
	f := webrtcFinding(t, BrowserReport{ExitV4: "5.6.7.8", SRFLX: []string{"1.2.3.4"}}, LocalFacts{})
	joined := strings.Join(f.Evidence, "\n")
	if !strings.Contains(joined, "5.6.7.8") || !strings.Contains(joined, "1.2.3.4") {
		t.Fatalf("bad 结论的 evidence 必须同时含 HTTP 出口与 srflx,得到 %v", f.Evidence)
	}
}
