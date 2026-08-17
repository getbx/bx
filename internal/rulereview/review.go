package rulereview

import "github.com/getbx/bx/internal/policy"

// riskySummary 是危险直连那一类要说的话。措辞与 internal/cli/direct.go 的
// directRuleRisk 保持同源语义:同一个判据在 add 那一侧和体检这一侧说的必须是同一件事。
const riskySummary = "公有云存储/CDN/开放子域平台——任何人都能注册它的子域;" +
	"留在直连白名单里 = 攻击者能用一个子域让你的真实 IP 暴露(去匿名化)。" +
	"建议只白名单品牌自控的顶级域。"

// Review 跑完四类判据。**纯函数:同样的输入永远给同样的输出。**
func Review(in Input) Report {
	direct := domainRules(in.Direct)

	var findings []Finding
	findings = append(findings, riskyFindings(direct)...)

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
