package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/getbx/bx/internal/rulereview"
)

// —— 死规则在 doctor 里怎么显示(2026-08-24)——

func deadReviewReport(t *testing.T, direct []string, history map[rulereview.RuleKey]rulereview.RuleCounts) rulereview.Report {
	t.Helper()
	return rulereview.Review(rulereview.Input{
		Direct:           direct,
		History:          history,
		HistoryUptime:    15 * 24 * time.Hour,
		HistoryDecisions: 25_000,
		HistoryVersions:  1,
	})
}

func lineByKey(lines []doctorFinding, key string) *doctorFinding {
	for i := range lines {
		if lines[i].Key == key {
			return &lines[i]
		}
	}
	return nil
}

// 1. 报出来的那一行要说清条数,并点名规则原文。
func TestDoctorReportsDeadRules(t *testing.T) {
	rep := deadReviewReport(t, []string{"*.never-used.example"}, map[rulereview.RuleKey]rulereview.RuleCounts{})
	lines := ruleReviewDoctorLines(rep)
	got := lineByKey(lines, deadRulesCheckName)
	if got == nil {
		t.Fatalf("死规则一个字都没说 —— 判据产出了 %d 条,渲染层却按 Class 字面枚举、"+
			"新的一类静默消失(本仓库记了四次的形状):%+v", rep.DeadCount, lines)
	}
	if !strings.Contains(got.Value, "*.never-used.example") {
		t.Errorf("没点名规则原文,用户无从核对:%q", got.Value)
	}
}

// 2. **跨了多个版本要说出来**,让用户对这份累计打折 —— 中间可能有几版的计数
// 行为并不一致。
func TestDoctorSaysHowManyVersionsTheHistorySpans(t *testing.T) {
	in := rulereview.Input{
		Direct:           []string{"*.never-used.example"},
		History:          map[rulereview.RuleKey]rulereview.RuleCounts{},
		HistoryUptime:    15 * 24 * time.Hour,
		HistoryDecisions: 25_000,
		HistoryVersions:  3,
	}
	got := lineByKey(ruleReviewDoctorLines(rulereview.Review(in)), deadRulesCheckName)
	if got == nil {
		t.Fatal("死规则那一行不见了")
	}
	if !strings.Contains(got.Value, "3") {
		t.Errorf("没说这份累计跨了几个版本:%q —— 用户无从判断该不该打折", got.Value)
	}
}

// 3. **「没查」必须说出来,不许静默缺席。**
//
// 这一类没查的情况比别的类多得多(Core 没在跑 / 溢出 / 门槛没到)。静默缺席会让
// 用户以为体检查过了这一项、而且没发现问题 —— 那是这份报告最不该给的印象。
func TestDoctorSaysWhyDeadRulesWereNotChecked(t *testing.T) {
	cases := []struct {
		name string
		in   rulereview.Input
		want string
	}{
		{
			name: "拿不到历史",
			in: rulereview.Input{
				Direct: []string{"*.x.example"}, History: nil,
				// 调用方给的理由**原样透传**(生产里今天没有任何地方设它,
				// 一直走 deadGate 的兜底;这一条钉的是"给了就用给的那句")。
				HistorySkipReason: "Core is not running",
			},
			want: "Core is not running",
		},
		{
			name: "跟踪表满过",
			in: rulereview.Input{
				Direct: []string{"*.x.example"}, History: map[rulereview.RuleKey]rulereview.RuleCounts{},
				HistoryUptime: 15 * 24 * time.Hour, HistoryDecisions: 25_000,
				HistoryOverflowed: true,
			},
			want: "overflowed",
		},
		{
			name: "门槛还没到",
			in: rulereview.Input{
				Direct: []string{"*.x.example"}, History: map[rulereview.RuleKey]rulereview.RuleCounts{},
				HistoryUptime: time.Hour, HistoryDecisions: 3,
			},
			want: "short of",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := lineByKey(ruleReviewDoctorLines(rulereview.Review(tc.in)), deadRulesCheckName)
			if got == nil {
				t.Fatalf("没查却一个字都没说 —— 用户会以为这一项查过了、而且没问题")
			}
			if got.Status != "info" {
				t.Errorf("Status = %q, want info —— 「还不能下结论」不是问题,"+
					"用 warn 会把一台正常的机器说成需注意", got.Status)
			}
			if !strings.Contains(got.Value, tc.want) {
				t.Errorf("没说清原因(想要含 %q):%q", tc.want, got.Value)
			}
		})
	}
}

// 4. **干净配置一个字都不说** —— 唯一例外是「没查」。
//
// 与「常驻面板不许变墙纸」同一条:一条对任何配置都会出现的行会被训练成噪声。
func TestDoctorSaysNothingWhenEveryRuleIsAlive(t *testing.T) {
	rep := deadReviewReport(t, []string{"*.busy.example"}, map[rulereview.RuleKey]rulereview.RuleCounts{
		{Source: "user_direct", Rule: "*.busy.example"}: {Attempts: 42},
	})
	if got := lineByKey(ruleReviewDoctorLines(rep), deadRulesCheckName); got != nil {
		t.Fatalf("每条规则都在用,却还是说了一句:%+v —— 恒出现的行会变成墙纸", got)
	}
}

// **判据产出的每一个 Class,都必须有渲染路径。**
//
// 渲染层按 Class 字面枚举,新加一类**不会有编译错误、也不会有测试转红** ——
// 它只是从输出里消失。这个仓库为这个形状栽过四次(同名 check 互相覆盖、
// hint 指向不存在的子命令、Kind 被整个丢掉、守卫在测试里重造一遍生产表达式)。
//
// 判据是**穷举 Class**:每一类都造一份只含它的报告,断言 ruleReviewDoctorLines
// 至少说了一句。加第六类时这条会红,那正是回来补渲染的时刻。
func TestEveryRuleReviewClassHasARenderingPath(t *testing.T) {
	classes := []struct {
		class rulereview.Class
		rep   rulereview.Report
	}{
		{rulereview.ClassRisky, rulereview.Report{
			Findings:   []rulereview.Finding{{Kind: "direct", Rule: "*.a", Class: rulereview.ClassRisky, Summary: "s"}},
			RiskyCount: 1,
		}},
		{rulereview.ClassShadowedByUserRule, rulereview.Report{
			Findings:            []rulereview.Finding{{Kind: "direct", Rule: "*.a", Class: rulereview.ClassShadowedByUserRule, Summary: "s", CoveredBy: "*.b"}},
			ShadowedByUserCount: 1,
		}},
		{rulereview.ClassOverriddenByOppositeKind, rulereview.Report{
			Findings:        []rulereview.Finding{{Kind: "direct", Rule: "*.a", Class: rulereview.ClassOverriddenByOppositeKind, Summary: "s", CoveredBy: "*.b"}},
			OverriddenCount: 1,
		}},
		{rulereview.ClassShadowedByBuiltinList, rulereview.Report{
			Findings:               []rulereview.Finding{{Kind: "direct", Rule: "*.a", Class: rulereview.ClassShadowedByBuiltinList, Summary: "s"}},
			ShadowedByBuiltinCount: 1,
			BuiltinListChecked:     true,
		}},
		{rulereview.ClassDead, rulereview.Report{
			Findings:    []rulereview.Finding{{Kind: "direct", Rule: "*.a", Class: rulereview.ClassDead, Summary: "s"}},
			DeadCount:   1,
			DeadChecked: true,
		}},
	}
	// 前置断言:这张表必须覆盖到最后一个 Class。少一个的话,这条守卫会漏掉
	// **恰好是新加的那一个** —— 而那正是它要守的东西。
	last := classes[len(classes)-1].class
	if rulereview.Class(int(last)+1).String() != "risky_direct" {
		t.Fatalf("Class 又多了一个(%q),这张表没跟上 —— 补进来,并确认它有渲染路径",
			rulereview.Class(int(last)+1).String())
	}
	for _, tc := range classes {
		if lines := ruleReviewDoctorLines(tc.rep); len(lines) == 0 {
			t.Errorf("Class %s 产出了 finding,而 doctor 一个字都没说 —— "+
				"渲染层按 Class 字面枚举,新加一类只会静默消失", tc.class)
		}
	}
}

// **CoveredBy 为空时不许留一个悬空的箭头。**
//
// 死规则没有「被谁盖住」这回事,而 summarizeFindings 原先无条件拼 `← ` ——
// 输出是 `*.a.example ← 、*.b.example ← `,一句没写完的话。
//
// **这个 bug 是肉眼看输出抓到的,不是测试抓到的**:上面那几条断言的是「这一行
// 含规则原文」,而它确实含 —— 于是全绿。断言「说了什么」与「说得像句人话」是
// 两件事,这条补的是后者。
func TestSummaryHasNoDanglingArrowWhenNothingCoversTheRule(t *testing.T) {
	rep := deadReviewReport(t, []string{"*.a.example", "*.b.example"}, map[rulereview.RuleKey]rulereview.RuleCounts{})
	got := lineByKey(ruleReviewDoctorLines(rep), deadRulesCheckName)
	if got == nil {
		t.Fatal("死规则那一行不见了")
	}
	if strings.Contains(got.Value, "← ") || strings.HasSuffix(strings.TrimSpace(got.Value), "←") {
		t.Errorf("留了一个悬空的箭头:%q", got.Value)
	}
	// 反向:有 CoveredBy 的那几类,箭头必须还在 —— 那是「被哪一条盖住」的唯一交代,
	// 少了它用户没法核对。
	shadow := rulereview.Review(rulereview.Input{Direct: []string{"*.a.com", "b.a.com"}})
	line := lineByKey(ruleReviewDoctorLines(shadow), "redundant rules")
	if line == nil {
		t.Fatal("这条测试的前提不成立:没造出被覆盖的那一类")
	}
	if !strings.Contains(line.Value, "←") {
		t.Errorf("有 CoveredBy 的那一类丢了箭头:%q —— 用户无从知道被哪一条盖住", line.Value)
	}
}
