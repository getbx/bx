package leakcheck

import (
	"strings"
	"testing"
)

func fingerprintBrowser(a, b string) BrowserReport {
	return BrowserReport{ExitV4: "203.0.113.9", CanvasA: a, CanvasB: b}
}

// 指纹防护这条问的不是「你的指纹是什么」,而是**「你的浏览器有没有在防」**。
//
// 「唯一性」算不出来:没有语料库就没有分母,编一个百分比比不报更糟。而「防没防」
// 是能观测的:Brave / Tor 每次会话给 canvas 加噪,同一次渲染两遍结果不同;
// 普通 Chrome 两遍逐字节相同。
func TestFingerprintProtectionRule(t *testing.T) {
	for _, tc := range []struct {
		name        string
		browser     BrowserReport
		want        Verdict
		wantMention string
	}{
		{
			name:        "两遍相同 —— 没有任何防护,这台机器每次都认得出",
			browser:     fingerprintBrowser("aaaa", "aaaa"),
			want:        Bad,
			wantMention: "same",
		},
		{
			name:    "两遍不同 —— 浏览器在加噪",
			browser: fingerprintBrowser("aaaa", "bbbb"),
			want:    OK,
		},
		{
			// canvas 被整个禁掉(读回空)也是一种防护,而且是更彻底的一种。
			name:    "canvas 读不出来",
			browser: fingerprintBrowser("", ""),
			want:    NotChecked,
		},
		{
			name:    "只拿到一遍",
			browser: fingerprintBrowser("aaaa", ""),
			want:    NotChecked,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := identityFinding(t, FindingFingerprint, tc.browser, LocalFacts{})
			if f.Verdict != tc.want {
				t.Fatalf("判定应为 %s,得到 %s(summary=%q)", tc.want, f.Verdict, f.Summary)
			}
			if tc.wantMention != "" && !strings.Contains(strings.ToLower(f.Summary), tc.wantMention) {
				t.Errorf("结论要说清依据 %q:%q", tc.wantMention, f.Summary)
			}
		})
	}
}

// **「网站看得到什么」永远是 Info,不许有极性。**
//
// 屏幕分辨率、显卡型号、UA 既不好也不坏 —— 判成 ok 会说成「安全」,判成 bad 会
// 说成「有问题」,而两者都是在给一件事强加一个它没有的极性。它也绝不进任何
// 异常计数,否则「加一条可见项」就等于「多一条告警」,加到第五条时告警就没人看了。
func TestSurfaceIsAlwaysInfoAndNeverCounts(t *testing.T) {
	browser := BrowserReport{
		ExitV4: "203.0.113.9", UserAgent: "Mozilla/5.0 …", Screen: "1512x982",
		Languages: []string{"zh-CN", "en"}, WebGLRenderer: "Apple M13 Pro",
		HardwareConcurrency: "12", Timezone: "Asia/Shanghai",
	}
	f := identityFinding(t, FindingSurface, browser, LocalFacts{})
	if f.Verdict != Info {
		t.Fatalf("判定 = %v,want Info —— 这一段的东西没有正确答案", f.Verdict)
	}
	if f.Section != SectionSurface {
		t.Fatalf("分段 = %v,want SectionSurface", f.Section)
	}
	joined := strings.Join(f.Evidence, " ")
	for _, want := range []string{"1512x982", "Apple M13 Pro", "zh-CN", "12"} {
		if !strings.Contains(joined, want) {
			t.Errorf("证据区必须原样列出 %q:%v", want, f.Evidence)
		}
	}

	rep := Judge(fixedTime(), browser, LocalFacts{})
	if rep.AnomalyCount != 0 || rep.IdentityCount != 0 {
		t.Errorf("Info 不许进任何计数,得到 anomaly=%d identity=%d", rep.AnomalyCount, rep.IdentityCount)
	}
}

// 一个字都没采到时不许摆一行空壳 —— 那是刚从菜单里删掉的占位符换个地方回来。
//
// **这里必须用一份「跑过但没给可见面字段」的上报**,不能用零值:零值是 Silent,
// 会先被「浏览器那一半从没到过」截走,于是这条测试测的是**另一个分支**,而空壳
// 那一支零覆盖 —— 变异验证当场抓到(把判据改成 `if false` 整套仍然全绿)。
func TestSurfaceSaysNotCheckedWhenTheBrowserRanButReportedNothing(t *testing.T) {
	browser := BrowserReport{ExitV4: "203.0.113.9"} // 跑过,但一个可见面字段都没有
	f := identityFinding(t, FindingSurface, browser, LocalFacts{})
	if f.Verdict != NotChecked {
		t.Fatalf("判定 = %v,want NotChecked:%q", f.Verdict, f.Summary)
	}
	if len(f.Evidence) != 0 {
		t.Errorf("什么都没采到却列出了证据:%v", f.Evidence)
	}
}

// 浏览器那一半从没到过时,与其余依赖浏览器的规则处置一致。
func TestSurfaceNotCheckedWhenBrowserNeverRan(t *testing.T) {
	f := identityFinding(t, FindingSurface, BrowserReport{}, LocalFacts{})
	if f.Verdict != NotChecked {
		t.Fatalf("判定 = %v,want NotChecked:%q", f.Verdict, f.Summary)
	}
}
