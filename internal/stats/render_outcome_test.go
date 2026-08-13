package stats

import (
	"strings"
	"testing"
)

// **只报值得行动的规则。**
//
// 列出全部规则没有价值(用户自己写的,他知道有哪些)。有价值的是「这一条正在
// 成片失败」—— 而判据必须同时看**比例**和**绝对数**:1/1 失败是 100% 但什么也
// 说明不了,8113/8113 才是一条死规则。
//
// 门槛偏保守是刻意的:这一行会常驻在用户每次 bx status 的输出里,一旦开始
// 出现无关紧要的规则,它就会和「Status Protected」一样被跳过不读。
func TestRuleIsWorthReporting(t *testing.T) {
	for _, tc := range []struct {
		name string
		r    RuleOutcome
		want bool
	}{
		{"死规则:成片失败", RuleOutcome{Source: "user_direct", Rule: "*.steamstatic.com", Attempts: 8113, Failures: 8113}, true},
		{"一半失败也值得看", RuleOutcome{Source: "user_direct", Rule: "*.x.com", Attempts: 100, Failures: 60}, true},
		{"健康规则", RuleOutcome{Source: "user_direct", Rule: "gsa.apple.com", Attempts: 900, Failures: 3}, false},
		// **这一条专门盯住比例门槛。** 上面那些用例的失败数都在绝对数门槛以下,
		// 于是把比例判据整个换成 `return true` 时全部照样通过 —— 变异测试当场
		// 抓到的洞。1000 次里失败 10 次是正常抖动,不是配置问题。
		{"失败数够多但比例很低,是抖动不是坏规则", RuleOutcome{Source: "user_direct", Rule: "*.w.com", Attempts: 1000, Failures: 10}, false},
		{"样本太小,100% 也不算数", RuleOutcome{Source: "user_direct", Rule: "*.y.com", Attempts: 2, Failures: 2}, false},
		{"没失败", RuleOutcome{Source: "user_direct", Rule: "*.z.com", Attempts: 500}, false},
		// **内建来源不报。** 它没有「哪一行」可点名,用户改不了;
		// 内建列表整体失败是另一类故障(隧道坏了),由失败总数那一行表达。
		{"内建列表不点名", RuleOutcome{Source: "china_domain", Attempts: 9000, Failures: 9000}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ruleWorthReporting(tc.r); got != tc.want {
				t.Errorf("ruleWorthReporting(%+v) = %v, want %v", tc.r, got, tc.want)
			}
		})
	}
}

// 一切正常时**一个字都不多说** —— 这是它不被训练成噪声的前提。
func TestRenderStaysSilentWhenNothingIsFailing(t *testing.T) {
	out := Render(Report{Snapshot: Snapshot{
		Proxy: 46447, Direct: 26186,
		Rules: []RuleOutcome{{Source: "user_direct", Rule: "gsa.apple.com", Attempts: 900, Failures: 1}},
	}})
	for _, forbidden := range []string{"失败", "gsa.apple.com"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("一切正常时输出里不该有 %q:\n%s", forbidden, out)
		}
	}
}

// 有死规则时必须点名到 config 里那一行,并给出下一步。
func TestRenderNamesTheFailingRule(t *testing.T) {
	out := Render(Report{ConfigPath: "/etc/bx/config.yaml", Snapshot: Snapshot{
		Proxy: 46447, Direct: 26186, DirectFailed: 8113,
		Rules: []RuleOutcome{
			{Source: "user_direct", Rule: "*.steamstatic.com", Attempts: 8113, Failures: 8113},
			{Source: "user_direct", Rule: "gsa.apple.com", Attempts: 900, Failures: 0},
		},
	}})
	for _, want := range []string{"*.steamstatic.com", "8113", "/etc/bx/config.yaml"} {
		if !strings.Contains(out, want) {
			t.Errorf("输出里没有 %q —— 归因的全部价值就是点名该改哪一行:\n%s", want, out)
		}
	}
	if strings.Contains(out, "gsa.apple.com") {
		t.Errorf("健康的规则不该出现:\n%s", out)
	}
}

// 失败总数那一行独立于规则归因:隧道坏了的表现是**代理**成片失败,
// 那时一条用户规则都不会被点名,而用户仍然需要看见「有东西在失败」。
func TestRenderShowsFailureTotalsEvenWithNoRuleToBlame(t *testing.T) {
	out := Render(Report{Snapshot: Snapshot{Proxy: 1000, ProxyFailed: 950}})
	if !strings.Contains(out, "950") {
		t.Errorf("代理失败总数没出现:\n%s", out)
	}
}

// 拿不到配置路径时(旧 Core、或该部署没有配置文件)仍要给出可读的下一步,
// 而不是打出一句 `改  的 rules`。
func TestRenderDegradesGracefullyWithoutAConfigPath(t *testing.T) {
	out := Render(Report{Snapshot: Snapshot{
		Direct: 8113, DirectFailed: 8113,
		Rules: []RuleOutcome{{Source: "user_direct", Rule: "*.steamstatic.com", Attempts: 8113, Failures: 8113}},
	}})
	if !strings.Contains(out, "*.steamstatic.com") {
		t.Fatalf("规则仍然要点名:\n%s", out)
	}
	if strings.Contains(out, "改  的") || strings.Contains(out, "改 的") {
		t.Errorf("路径缺失时打出了残缺的句子:\n%s", out)
	}
}
