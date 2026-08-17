package rulereview

import (
	"github.com/getbx/bx/internal/policy"
	"github.com/getbx/bx/internal/route"
)

// riskySummary 是危险直连那一类要说的话。措辞与 internal/cli/direct.go 的
// directRuleRisk 保持同源语义:同一个判据在 add 那一侧和体检这一侧说的必须是同一件事。
const riskySummary = "公有云存储/CDN/开放子域平台——任何人都能注册它的子域;" +
	"留在直连白名单里 = 攻击者能用一个子域让你的真实 IP 暴露(去匿名化)。" +
	"建议只白名单品牌自控的顶级域。"

// Review 跑完四类判据。**纯函数:同样的输入永远给同样的输出。**
func Review(in Input) Report {
	direct := domainRules(in.Direct)
	proxy := domainRules(in.Proxy)

	var findings []Finding
	findings = append(findings, riskyFindings(direct)...)
	findings = append(findings, shadowedByUserFindings("direct", direct)...)
	findings = append(findings, shadowedByUserFindings("proxy", proxy)...)
	findings = append(findings, overriddenFindings(direct, proxy)...)

	// **global 下 china 列表整个不生效**,那时「被它覆盖」这个结论是错的 ——
	// spec 写完当天的真机实测报出 22 条,而那 22 条全都在干活。
	// 读的是 config.Global,不是 config.Mode(后者只有 host|router)。
	//
	// 压制的**只有这一类**:另外三类模式无关,一条都不许少。
	checked := false
	skip := in.ChinaSkipReason
	switch {
	case in.GlobalProxy:
		skip = "global 模式下内建 china 列表整个不生效,这一类没有比对"
	case in.China == nil:
		if skip == "" {
			skip = "没拿到内建 china 列表,这一类没有比对"
		}
	default:
		checked = true
		skip = ""
		findings = append(findings, shadowedByBuiltinFindings("direct", direct, in.China)...)
		findings = append(findings, shadowedByBuiltinFindings("proxy", proxy, in.China)...)
	}

	return NewReport(findings, checked, skip)
}

// shadowedByBuiltinFindings 找出已经被内建 china 直连列表覆盖的手写规则。
//
// 调用方负责保证这一类该不该跑(见 Review 的 gating)——本函数不认识 mode。
func shadowedByBuiltinFindings(kind string, rules []domainRule, china *route.DomainSet) []Finding {
	var out []Finding
	for _, r := range rules {
		covering, ok := china.MatchRule(r.norm)
		if !ok {
			continue
		}
		summary := "已在内建 china 直连列表里,这条手写的没有额外作用。"
		if kind == "proxy" {
			// proxy 规则命中 china 列表不是冗余 —— 它是**故意的例外**:
			// 用户就是要把一个内建列表判直连的域名扳回隧道。说反了会让他删掉它。
			summary = "内建 china 列表把它判为直连,而你这条把它扳回隧道——这是生效中的例外,不是冗余。"
		}
		out = append(out, Finding{
			Kind:      kind,
			Rule:      r.raw,
			Class:     ClassShadowedByBuiltinList,
			Summary:   summary,
			CoveredBy: covering,
		})
	}
	return out
}

// riskyFindings 只看 direct 表:DirectRisk 的整个语义是「把它放进**直连**白名单
// 会怎样」,对 proxy 规则问这个问题没有意义。
func riskyFindings(direct []domainRule) []Finding {
	var out []Finding
	for _, r := range direct {
		if !policy.DirectRisk(r.norm) {
			continue
		}
		out = append(out, Finding{
			Kind:    "direct",
			Rule:    r.raw,
			Class:   ClassRisky,
			Summary: riskySummary,
		})
	}
	return out
}

// shadowedByUserFindings 找出**同一张表**里被更宽的一条盖住的规则。
//
// 判据:把**除它自己以外**的其余规则建成一个 DomainSet,看它的域名命不命中。
// 「除它自己以外」是承重的 —— DomainSet.Match 对自身恒为真,不排除就是每一条
// 都报冗余;而调用方拿到的是 domainRules 去过重的表,所以也不会有两条互相指认。
//
// 每条都重建一次 DomainSet 是 O(n²),而 n 是用户手写的规则数(实测量级 24)。
// 换成一次建表 + 反查,要额外维护「哪个后缀来自哪条」的簿记,而那正是出错的地方。
func shadowedByUserFindings(kind string, rules []domainRule) []Finding {
	var out []Finding
	for i, r := range rules {
		others := make([]string, 0, len(rules)-1)
		for j, o := range rules {
			if i == j {
				continue
			}
			others = append(others, o.raw)
		}
		covering, ok := route.NewDomainSet(others).MatchRule(r.norm)
		if !ok {
			continue
		}
		out = append(out, Finding{
			Kind:      kind,
			Rule:      r.raw,
			Class:     ClassShadowedByUserRule,
			Summary:   "已被你自己更宽的一条覆盖,删掉它不会改变任何流量的去向。",
			CoveredBy: covering,
		})
	}
	return out
}

// overriddenFindings 找出**被另一张表压住、于是从来没生效过**的规则。
//
// **只有一个方向。** route.Router.Explain 先查 UserProxy 再查 UserDirect
// (internal/route/explain.go:78),中间没有任何按具体程度排序的步骤,所以:
//
//	· direct 规则被更宽的 proxy 规则吃掉 ⇒ 它永远拿不到 Direct 判定;
//	· proxy 规则**不会**被更宽的 direct 规则吃掉 —— 它先被查到,照样生效。
//
// 把方向搞反的后果不是少报一条,是叫用户删掉一条正在工作的强制走隧道规则,
// 也就是把流量从隧道里赶出去。这个顺序**不许靠读代码断言** ——
// internal/supervisor/ruleprecedence_test.go 用真的 route.Router 证明它。
func overriddenFindings(direct, proxy []domainRule) []Finding {
	if len(proxy) == 0 {
		return nil
	}
	raw := make([]string, 0, len(proxy))
	for _, p := range proxy {
		raw = append(raw, p.raw)
	}
	proxySet := route.NewDomainSet(raw)

	var out []Finding
	for _, d := range direct {
		covering, ok := proxySet.MatchRule(d.norm)
		if !ok {
			continue
		}
		out = append(out, Finding{
			Kind:      "direct",
			Rule:      d.raw,
			Class:     ClassOverriddenByOppositeKind,
			Summary:   "被 proxy 表里更宽的一条压住,从来没有生效过——bx 先查 proxy 再查 direct,没有「更具体的优先」。要它生效就得收窄或删掉压住它的那一条。",
			CoveredBy: covering,
		})
	}
	return out
}
