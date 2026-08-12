package leakcheck

import (
	"strings"
	"testing"
)

// 时区与出口国的矛盾,是这个页面**唯一 browserleaks 做不到**的一类判断:
// 它知道出口在哪儿。browserleaks 会告诉你 `Timezone: Asia/Shanghai`,
// 但它不会告诉你这跟你的出口自相矛盾 —— 而后者才是能把人钉住的那件事。
func TestTimezoneRule(t *testing.T) {
	for _, tc := range []struct {
		name        string
		browser     BrowserReport
		want        Verdict
		wantMention string
	}{
		{
			name:        "出口在美国而时钟在上海 —— 这个组合本身就是标记",
			browser:     BrowserReport{ExitCountry: "US", Timezone: "Asia/Shanghai"},
			want:        Bad,
			wantMention: "Asia/Shanghai",
		},
		{
			name:        "出口在德国而时钟在东京",
			browser:     BrowserReport{ExitCountry: "DE", Timezone: "Asia/Tokyo"},
			want:        Bad,
			wantMention: "Asia/Tokyo",
		},
		{
			name:        "出口与时钟同在一个大区",
			browser:     BrowserReport{ExitCountry: "US", Timezone: "America/Los_Angeles"},
			want:        OK,
			wantMention: "America/Los_Angeles",
		},
		{
			// 美国横跨 America 与 Pacific(檀香山)。一个国家可以有多个合法大区,
			// 判据必须认全,否则夏威夷用户会被无端报红。
			name:    "美国的太平洋时区不是矛盾",
			browser: BrowserReport{ExitCountry: "US", Timezone: "Pacific/Honolulu"},
			want:    OK,
		},
		{
			// 俄罗斯横跨欧亚。同理。
			name:    "俄罗斯横跨欧亚,两边都不算矛盾",
			browser: BrowserReport{ExitCountry: "RU", Timezone: "Asia/Novosibirsk"},
			want:    OK,
		},
		{
			name:        "出口国没问出来",
			browser:     BrowserReport{Timezone: "Asia/Shanghai", TraceErr: "network error"},
			want:        NotChecked,
			wantMention: "network error",
		},
		{
			name:    "浏览器没给时区",
			browser: BrowserReport{ExitCountry: "US"},
			want:    NotChecked,
		},
		{
			// **认不出的国家必须说认不出。** 编一个大区去比,得到的是一条
			// 随机的红或绿,而两者都比不报更糟。
			name:        "表里没有这个国家",
			browser:     BrowserReport{ExitCountry: "ZZ", Timezone: "Asia/Shanghai"},
			want:        NotChecked,
			wantMention: "ZZ",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := identityFinding(t, FindingTimezone, tc.browser, LocalFacts{})
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

// **同一个大区不等于没问题,措辞不许说成没问题。**
//
// 出口在美国、时钟在 America/Sao_Paulo 是同一个 IANA 大区,而它显然仍是个矛盾。
// 这条判据只做到大区粒度,所以它的 ok 必须说清楚自己只查到哪一层 —— 一句
// 「一切正常」会让用户以为这件事已经查完了。
func TestTimezoneOKAdmitsHowFarItLooked(t *testing.T) {
	f := identityFinding(t, FindingTimezone,
		BrowserReport{ExitCountry: "US", Timezone: "America/Los_Angeles"}, LocalFacts{})
	if f.Verdict != OK {
		t.Fatalf("want OK, got %v", f.Verdict)
	}
	lowered := strings.ToLower(f.Summary)
	if !strings.Contains(lowered, "region") && !strings.Contains(lowered, "continent") {
		t.Errorf("ok 的措辞必须说明它只比到大区这一层,否则用户会以为这件事查完了:%q", f.Summary)
	}
}

// 浏览器那一半从没到过时,与其余依赖浏览器的规则处置一致。
func TestTimezoneNotCheckedWhenBrowserNeverRan(t *testing.T) {
	f := identityFinding(t, FindingTimezone, BrowserReport{}, LocalFacts{})
	if f.Verdict != NotChecked {
		t.Fatalf("判定 = %v,want NotChecked:%q", f.Verdict, f.Summary)
	}
}
