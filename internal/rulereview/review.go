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

	return NewReport(findings, false, in.ChinaSkipReason)
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
