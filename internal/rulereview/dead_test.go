package rulereview

import (
	"strings"
	"testing"
	"time"
)

// —— 死规则(2026-08-24)——
//
// 「这条规则从来没命中过」是这份体检里**唯一一条要跨重启才敢说的话**。
// 单次运行的 0 次什么也说明不了:一台刚重连的机器上每条规则都是 0 次。
// 所以判据有三道门槛,而**任何一道问不出来就整类不判**。

// deadInput 造一份「三门槛都满足」的输入,再由各条测试按需破坏其中一项。
func deadInput(direct []string, history map[RuleKey]RuleCounts) Input {
	return Input{
		Direct:           direct,
		History:          history,
		HistoryUptime:    deadMinUptime + time.Hour,
		HistoryDecisions: deadMinDecisions + 1,
		HistoryVersions:  2,
	}
}

func deadFindingsOf(rep Report) []Finding {
	var out []Finding
	for _, f := range rep.Findings {
		if f.Class == ClassDead {
			out = append(out, f)
		}
	}
	return out
}

// 1. 三门槛全满足 ⇒ 报死,点名规则**原文**。
func TestDeadReportedWhenAllThresholdsAreMet(t *testing.T) {
	rep := Review(deadInput([]string{"*.never-used.example"}, map[RuleKey]RuleCounts{
		{Source: "user_direct", Rule: "*.other.example"}: {Attempts: 9},
	}))
	got := deadFindingsOf(rep)
	if len(got) != 1 {
		t.Fatalf("死规则 = %d 条, want 1:%+v", len(got), rep.Findings)
	}
	if got[0].Rule != "*.never-used.example" {
		t.Errorf("点名的不是配置里那一行的原文:%q", got[0].Rule)
	}
	if got[0].CoveredBy != "" {
		t.Errorf("死规则没有「被谁盖住」这回事,CoveredBy 应当为空:%q", got[0].CoveredBy)
	}
	if !rep.DeadChecked {
		t.Error("DeadChecked 应当为真(三门槛都满足,确实查过了)")
	}
	if rep.DeadCount != 1 {
		t.Errorf("DeadCount = %d, want 1", rep.DeadCount)
	}
}

// 2. **时长不够 ⇒ 一条都不报。** 刚重连的机器上每条规则都是 0 次,报出来全是错的。
func TestDeadNotReportedWhenUptimeIsTooShort(t *testing.T) {
	in := deadInput([]string{"*.never-used.example"}, map[RuleKey]RuleCounts{})
	in.HistoryUptime = deadMinUptime - time.Minute
	rep := Review(in)
	if got := deadFindingsOf(rep); len(got) != 0 {
		t.Fatalf("累计运行时长不够却报了死规则 %+v —— 刚重连的机器上每条规则都是 0 次", got)
	}
	if rep.DeadChecked {
		t.Error("门槛没到时 DeadChecked 必须为假 —— 「还不能下结论」不是「查过了,没有」")
	}
	if rep.DeadSkipReason == "" {
		t.Error("没说为什么不判 —— 用户会以为体检查过了这一项")
	}
}

// 3. **判定数不够 ⇒ 一条都不报。** 一台开着挂机、几乎没有流量的机器,时长会到,
// 而它的规则一条都没被真正考验过。
func TestDeadNotReportedWhenTooFewDecisions(t *testing.T) {
	in := deadInput([]string{"*.never-used.example"}, map[RuleKey]RuleCounts{})
	in.HistoryDecisions = deadMinDecisions - 1
	rep := Review(in)
	if got := deadFindingsOf(rep); len(got) != 0 {
		t.Fatalf("判定数不够却报了死规则 %+v —— 那台机器的规则根本没被考验过", got)
	}
	if rep.DeadChecked {
		t.Error("门槛没到时 DeadChecked 必须为假")
	}
}

// 4. **History == nil ⇒ 没查**,而不是「零条」。
func TestDeadNotCheckedWhenThereIsNoHistory(t *testing.T) {
	in := deadInput([]string{"*.never-used.example"}, nil)
	in.HistorySkipReason = "Core 没在跑"
	rep := Review(in)
	if rep.DeadChecked {
		t.Error("拿不到历史却说查过了")
	}
	if rep.DeadCount != 0 {
		t.Errorf("DeadCount = %d, want 0", rep.DeadCount)
	}
	if !strings.Contains(rep.DeadSkipReason, "Core 没在跑") {
		t.Errorf("没把调用方给的理由透传出去:%q", rep.DeadSkipReason)
	}
}

// 5. **跟踪表满过 ⇒ 整类没查。这一条最要紧。**
//
// 表满之后新规则不再被记,于是「历史里没有这条」与「有这条、attempts==0」
// **在数据上完全无法区分** —— 而前者是「没被记过」,后者才是「从没命中」。
// 不区分就会把一条正在工作的规则报成死的,用户照着删掉。
func TestDeadNotCheckedWhenTheTrackingTableOverflowed(t *testing.T) {
	in := deadInput([]string{"*.never-used.example"}, map[RuleKey]RuleCounts{})
	in.HistoryOverflowed = true
	rep := Review(in)
	if got := deadFindingsOf(rep); len(got) != 0 {
		t.Fatalf("跟踪表满过却仍然判死 %+v —— 表满时「没有条目」与「从没命中」"+
			"在数据上无法区分,判死会让用户删掉一条正在工作的规则", got)
	}
	if rep.DeadChecked {
		t.Error("溢出时 DeadChecked 必须为假")
	}
	if !strings.Contains(rep.DeadSkipReason, "满") && !strings.Contains(rep.DeadSkipReason, "溢出") {
		t.Errorf("没点名溢出这个原因:%q", rep.DeadSkipReason)
	}
}

// 6. **历史里没有该规则的条目(未溢出)⇒ 报死。**
// 与第 5 条合起来才完整:溢出时不敢判,没溢出时敢判 —— 否则这个功能永远不生效。
func TestDeadReportedWhenTheRuleHasNoEntryAndNoOverflow(t *testing.T) {
	rep := Review(deadInput([]string{"*.never-used.example"}, map[RuleKey]RuleCounts{}))
	if got := deadFindingsOf(rep); len(got) != 1 {
		t.Fatalf("没有条目、也没溢出,却不判死 —— 这个功能于是永远不生效:%+v", rep.Findings)
	}
}

// 7. **attempts > 0 ⇒ 不报。**
func TestDeadNotReportedWhenTheRuleHasBeenHit(t *testing.T) {
	rep := Review(deadInput([]string{"*.busy.example"}, map[RuleKey]RuleCounts{
		{Source: "user_direct", Rule: "*.busy.example"}: {Attempts: 1},
	}))
	if got := deadFindingsOf(rep); len(got) != 0 {
		t.Fatalf("命中过 1 次的规则被判死:%+v", got)
	}
}

// **规则原文与历史里那一条的比对必须容得下大小写与 `*.` 前缀的差异。**
//
// 对不上的后果是**假阳性**:一条天天在用的规则被报成「从来没命中过」,用户照着
// 删掉。归一化比对是这个功能里代价最不对称的一处。
func TestDeadMatchesHistoryAcrossNormalisation(t *testing.T) {
	rep := Review(deadInput([]string{"*.Busy.Example."}, map[RuleKey]RuleCounts{
		{Source: "user_direct", Rule: "busy.example"}: {Attempts: 5},
	}))
	if got := deadFindingsOf(rep); len(got) != 0 {
		t.Fatalf("大小写/前缀差异让一条在用的规则被判死:%+v —— "+
			"用户会照着删掉一条天天在工作的规则", got)
	}
}

// **direct 与 proxy 两张表分开查。** 同一条原文可以同时在两张表里、语义相反;
// 只按规则原文比对会让 proxy 那条的命中把 direct 那条也「救活」。
func TestDeadKeepsDirectAndProxyApart(t *testing.T) {
	in := deadInput([]string{"*.both.example"}, map[RuleKey]RuleCounts{
		{Source: "user_proxy", Rule: "*.both.example"}: {Attempts: 9},
	})
	in.Proxy = []string{"*.both.example"}
	rep := Review(in)
	got := deadFindingsOf(rep)
	if len(got) != 1 || got[0].Kind != "direct" {
		t.Fatalf("死规则 = %+v, want 只有 direct 那条(proxy 那条命中过 9 次)", got)
	}
}

// 8. DeadVersionsSpanned 透传。用户据此对这份累计打折。
func TestDeadReportCarriesTheVersionSpan(t *testing.T) {
	rep := Review(deadInput([]string{"*.never-used.example"}, map[RuleKey]RuleCounts{}))
	if rep.DeadVersionsSpanned != 2 {
		t.Errorf("DeadVersionsSpanned = %d, want 2", rep.DeadVersionsSpanned)
	}
}

// 9. **五个计数各自独立,没有任何一处求和。**
//
// 与 Report 没有 TotalCount 是同一条纪律:合成一个数之后它对任何成熟配置都不为零,
// 于是被训练成噪声,把真正要紧的那一类一起淹掉。
func TestDeadCountNeverMergesWithTheOtherFour(t *testing.T) {
	in := deadInput([]string{
		"*.myqcloud.com",     // risky
		"*.a.com", "b.a.com", // shadowed_by_user_rule(后者被前者盖住)
		"*.never-used.example", // dead
	}, map[RuleKey]RuleCounts{})
	in.Proxy = []string{"*.c.com"}
	in.Direct = append(in.Direct, "x.c.com") // overridden_by_opposite_kind
	rep := Review(in)

	if rep.RiskyCount == 0 || rep.ShadowedByUserCount == 0 || rep.OverriddenCount == 0 || rep.DeadCount == 0 {
		t.Fatalf("这条测试的前提不成立(四类没有同时出现):risky=%d shadowed=%d overridden=%d dead=%d",
			rep.RiskyCount, rep.ShadowedByUserCount, rep.OverriddenCount, rep.DeadCount)
	}
	if rep.DeadCount != len(deadFindingsOf(rep)) {
		t.Errorf("DeadCount(%d)与实际死规则条数(%d)对不上 —— 有地方把别的类算进来了",
			rep.DeadCount, len(deadFindingsOf(rep)))
	}
	// 反向:别的四个计数里不许混进死规则。
	if rep.RiskyCount != 1 {
		t.Errorf("RiskyCount = %d, want 1 —— 死规则被算进危险那一类了", rep.RiskyCount)
	}
}
