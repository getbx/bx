package rulereview

import "testing"

// 起因就是这一条:项目所有者的配置里有 *.myqcloud.com,而 policy.DirectRisk 全仓
// 只有 bx direct add 一个调用点 —— 不管它是绕过守卫直接改 YAML 加的,还是守卫上线
// 之前就在的,此后再也没有任何东西会提醒他。
func TestRiskyDirectRuleIsReported(t *testing.T) {
	rep := Review(Input{Direct: []string{"*.myqcloud.com", "*.qq.com"}})

	if rep.RiskyCount != 1 {
		t.Fatalf("RiskyCount = %d, want 1;findings=%+v", rep.RiskyCount, rep.Findings)
	}
	if len(rep.Findings) != 1 {
		t.Fatalf("findings 数 = %d, want 1: %+v", len(rep.Findings), rep.Findings)
	}
	f := rep.Findings[0]
	if f.Class != ClassRisky {
		t.Errorf("Class = %v, want ClassRisky", f.Class)
	}
	// **原文,不是归一化形式。** 报 myqcloud.com 而他写的是 '*.myqcloud.com',
	// 他会去配置里搜一个搜不到的串。
	if f.Rule != "*.myqcloud.com" {
		t.Errorf("Rule = %q, want %q(必须是配置里那一行的原文)", f.Rule, "*.myqcloud.com")
	}
	if f.Kind != "direct" {
		t.Errorf("Kind = %q, want direct", f.Kind)
	}
	if f.Summary == "" {
		t.Error("Summary 是空的 —— 一条不说明理由的告警,用户没有理由信它")
	}
}

// proxy 那张表不受这条判据管:强制走隧道的域名不存在「去匿名化」这回事,
// DirectRisk 的整个语义是「把它放进**直连**白名单会怎样」。
func TestRiskyJudgementDoesNotApplyToProxyRules(t *testing.T) {
	rep := Review(Input{Proxy: []string{"*.myqcloud.com"}})
	if rep.RiskyCount != 0 {
		t.Fatalf("proxy 规则被判成了危险直连:%+v", rep.Findings)
	}
}

// rules[].direct / rules[].proxy 里**可以写 CIDR 和裸 IP**(supervisor.BuildRouter 的
// asCIDR 会把它们分到 CIDRSet 去)。把它们当域名喂进判据是无意义的,而更要紧的是
// 别让它们产出任何**看起来言之凿凿的**结论。
//
// 这里刻意只断言「一条都不报」而不是「报得对」:本包不复制 asCIDR 那份判定
// (判定只有一份是本仓库的纪律),代价是遇到网络字面量时**沉默**——沉默是漏报,
// 而漏报的代价远小于对着一条 CIDR 说「这条规则可以删」。
func TestNetworkLiteralsAreSkippedEntirely(t *testing.T) {
	rep := Review(Input{
		Direct: []string{"10.0.0.0/8", "192.168.1.1", "2001:db8::/32", "::1"},
		Proxy:  []string{"172.16.0.0/12"},
	})
	if len(rep.Findings) != 0 {
		t.Fatalf("网络字面量产出了结论,而本包读不懂它们:%+v", rep.Findings)
	}
}

// 空输入必须是干净报告,而不是 nil panic。
func TestEmptyInputIsCleanReport(t *testing.T) {
	rep := Review(Input{})
	if len(rep.Findings) != 0 || rep.RiskyCount != 0 {
		t.Fatalf("空配置报出了东西:%+v", rep)
	}
}

// spec 里那次真机实测:11 条是被用户自己更宽的一条盖住的。这一类**模式无关** ——
// 任何 mode 下都成立,所以它是四类里唯一一条可以无条件说出口的「可以删」。
func TestShadowedByUserOwnBroaderRule(t *testing.T) {
	rep := Review(Input{Direct: []string{"*.apple.com", "ocsp.apple.com", "*.qq.com"}})

	if rep.ShadowedByUserCount != 1 {
		t.Fatalf("ShadowedByUserCount = %d, want 1;findings=%+v", rep.ShadowedByUserCount, rep.Findings)
	}
	f := rep.Findings[0]
	if f.Rule != "ocsp.apple.com" {
		t.Errorf("被盖住的应该是窄的那条,got Rule=%q", f.Rule)
	}
	// 一个只说「这条冗余」而不肯说「被哪一条盖住」的报告,用户没法核对。
	if f.CoveredBy != "*.apple.com" {
		t.Errorf("CoveredBy = %q, want %q(必须是原文)", f.CoveredBy, "*.apple.com")
	}
	if f.Class != ClassShadowedByUserRule {
		t.Errorf("Class = %v, want ClassShadowedByUserRule", f.Class)
	}
}

// **自遮蔽陷阱。** DomainSet.Match("a.com") 对含 a.com 的集合恒为真,所以
// 「拿全表比每一条」会把每一条都报成冗余。被测那条必须先被排除掉。
func TestARuleIsNeverShadowedByItself(t *testing.T) {
	rep := Review(Input{Direct: []string{"*.apple.com"}, Proxy: []string{"*.qq.com"}})
	if len(rep.Findings) != 0 {
		t.Fatalf("规则把自己报成了冗余:%+v", rep.Findings)
	}
}

// **互相指认陷阱。** 两条完全重复的规则各自被对方覆盖,天真实现会说两条都能删;
// 用户照做,这条规则整个没了。去重后只留第一条,且不产出任何 finding ——
// 本计划不做「重复规则」这一类(spec 没有它),沉默是刻意的。
func TestExactDuplicatesDoNotAccuseEachOther(t *testing.T) {
	rep := Review(Input{Direct: []string{"*.apple.com", "apple.com", "*.APPLE.com."}})
	if len(rep.Findings) != 0 {
		t.Fatalf("重复规则互相指认成冗余:%+v", rep.Findings)
	}
}

// 跨表不算这一类:direct 里的 ocsp.apple.com 被 proxy 里的 *.apple.com 压住,
// 那是另一回事(Task 3),语义相反,绝不能合并计数。
func TestShadowByUserRuleIsWithinTheSameKindOnly(t *testing.T) {
	rep := Review(Input{Direct: []string{"ocsp.apple.com"}, Proxy: []string{"*.apple.com"}})
	if rep.ShadowedByUserCount != 0 {
		t.Fatalf("跨表被算进了同表冗余:%+v", rep.Findings)
	}
}

// proxy 表内部同样要查。
func TestShadowByUserRuleAppliesToProxyTableToo(t *testing.T) {
	rep := Review(Input{Proxy: []string{"*.google.com", "mail.google.com"}})
	if rep.ShadowedByUserCount != 1 {
		t.Fatalf("proxy 表内部的冗余没被查出来:%+v", rep.Findings)
	}
	if rep.Findings[0].Kind != "proxy" {
		t.Errorf("Kind = %q, want proxy", rep.Findings[0].Kind)
	}
}
