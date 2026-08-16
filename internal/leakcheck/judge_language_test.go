package leakcheck

import (
	"strings"
	"testing"
)

func languageReport(country string, langs ...string) BrowserReport {
	return BrowserReport{ExitV4: "203.0.113.9", ExitCountry: country, Languages: langs}
}

// **只在能证明时才判。** 与「国旗只在能证明时给」同一条纪律:出口国问不出来、
// 或者 bx 不带那个国家的语言表时,编一个答案比不报更糟。
func TestLanguageOnlyJudgesWhenItCanProve(t *testing.T) {
	for _, tc := range []struct {
		name string
		rep  BrowserReport
	}{
		{"出口国没问出来", languageReport("", "zh-CN")},
		{"浏览器没报语言", languageReport("JP")},
		{"bx 不带这个国家的语言表", languageReport("ZW", "zh-CN")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := judgeLanguage(tc.rep, LocalFacts{})
			if f.Verdict != NotChecked {
				t.Fatalf("判成了 %v:%q —— 证不出来就不许判", f.Verdict, f.Summary)
			}
			if !strings.Contains(f.Summary, "Not checked") {
				t.Errorf("没说清是没查:%q", f.Summary)
			}
		})
	}
}

// **英语一律放过。** 它是网上的通用语,从任何出口报 en 都不扎眼;把它判成不符
// 会让一大批完全正常的人天天看到红字,而红字看多了就没人看了。
func TestEnglishIsNeverFlagged(t *testing.T) {
	for _, country := range []string{"JP", "DE", "RU", "BR"} {
		f := judgeLanguage(languageReport(country, "en-US", "en"), LocalFacts{})
		if f.Verdict != OK {
			t.Errorf("%s 出口 + 英语判成了 %v:%q", country, f.Verdict, f.Summary)
		}
	}
}

// 对得上就 ok —— 少了这条,上面那些可以靠「永远不判 bad」满足。
func TestMatchingLanguageIsOK(t *testing.T) {
	for _, tc := range [][2]string{{"JP", "ja"}, {"DE", "de-DE"}, {"TW", "zh-TW"}, {"SG", "zh-CN"}} {
		f := judgeLanguage(languageReport(tc[0], tc[1]), LocalFacts{})
		if f.Verdict != OK {
			t.Errorf("%s + %s 判成了 %v:%q", tc[0], tc[1], f.Verdict, f.Summary)
		}
	}
}

// **对不上要判 bad,并且说清它不是泄漏、以及怎么办。**
//
// 说清「不是泄漏」很要紧:这一条不是「有东西漏出去了」,而是「你和这个出口的
// 其他人不一样」。混为一谈会让用户以为 bx 漏了。
func TestMismatchIsFlaggedWithAFix(t *testing.T) {
	f := judgeLanguage(languageReport("JP", "zh-CN", "en"), LocalFacts{})
	if f.Verdict != Bad {
		t.Fatalf("中文浏览器 + 日本出口判成了 %v:%q", f.Verdict, f.Summary)
	}
	if !strings.Contains(f.Summary, "Nothing leaked") {
		t.Errorf("没说清它不是泄漏:%q", f.Summary)
	}
	if !strings.Contains(f.Summary, "language list") {
		t.Errorf("没给出怎么办:%q", f.Summary)
	}
	// 证据要同时给出两边,否则用户无从判断这条结论对不对。
	joined := strings.Join(f.Evidence, " | ")
	if !strings.Contains(joined, "JP") || !strings.Contains(joined, "zh") {
		t.Errorf("证据没给全两边:%q", joined)
	}
}

// 只看**第一条**语言:那是网站真正拿去决定内容的那个,后面几条是回落。
func TestPrimaryLanguageIsTheFirstOne(t *testing.T) {
	for _, tc := range []struct {
		in   []string
		want string
	}{
		{[]string{"zh-CN", "en"}, "zh"},
		{[]string{"en-US"}, "en"},
		{[]string{"pt_BR"}, "pt"},
		{[]string{"", "  ", "ja"}, "ja"},
		{nil, ""},
	} {
		if got := primaryLanguage(tc.in); got != tc.want {
			t.Errorf("primaryLanguage(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// **没有浏览器时回落到系统值。**
//
// `bx leak-check`(非交互那条,MCP 与脚本在用)从来不会有浏览器上报 —— 在此
// 之前这两条对它永远是「没查」,而系统的时区与语言本机就读得到。
func TestSystemValuesAreUsedWhenTheBrowserIsSilent(t *testing.T) {
	// 浏览器只报了出口(页面的 trace 那一半),没报语言/时区。
	browser := BrowserReport{ExitV4: "203.0.113.9", ExitCountry: "JP"}
	local := LocalFacts{SystemLanguages: []string{"zh_CN"}, SystemTimezone: "Asia/Shanghai"}

	lang := judgeLanguage(browser, local)
	if lang.Verdict != Bad {
		t.Fatalf("系统语言没被用上:%v %q", lang.Verdict, lang.Summary)
	}
	// **必须说清这次用的是哪一个** —— 拿系统值去断言「网站看到的是这个」是冒认。
	if !strings.Contains(strings.Join(lang.Evidence, " | "), "system language") {
		t.Errorf("没点明来源是系统:%v", lang.Evidence)
	}

	zone := judgeTimezone(browser, local)
	if zone.Verdict == NotChecked {
		t.Fatalf("系统时区没被用上:%q", zone.Summary)
	}
	if !strings.Contains(strings.Join(zone.Evidence, " | "), "system time zone") {
		t.Errorf("没点明来源是系统:%v", zone.Evidence)
	}
}

// **浏览器的值优先。** 网站看到的是浏览器报的那个,系统值只是它的来源,
// 两者可以不同(浏览器语言是单独设的)。
func TestBrowserValueWinsOverTheSystemOne(t *testing.T) {
	browser := BrowserReport{ExitV4: "203.0.113.9", ExitCountry: "JP", Languages: []string{"ja"}}
	local := LocalFacts{SystemLanguages: []string{"zh_CN"}}
	f := judgeLanguage(browser, local)
	if f.Verdict != OK {
		t.Fatalf("浏览器报的 ja 没被优先:%v %q", f.Verdict, f.Summary)
	}
	if !strings.Contains(strings.Join(f.Evidence, " | "), "browser language") {
		t.Errorf("来源标成了系统:%v", f.Evidence)
	}
}

// 两边都没有时如实说没查 —— 不许拿一个空串去比。
func TestNeitherSourceMeansNotChecked(t *testing.T) {
	f := judgeLanguage(BrowserReport{ExitV4: "203.0.113.9", ExitCountry: "JP"}, LocalFacts{})
	if f.Verdict != NotChecked {
		t.Fatalf("两边都没有却判了 %v:%q", f.Verdict, f.Summary)
	}
}
