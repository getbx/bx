package cli

import (
	"fmt"
	"strings"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/route"
	"github.com/getbx/bx/internal/rulereview"
)

// doctorFinding 是一行 doctor 输出的三段式,与 doctorLine / rep.addCheck 的形参同构。
// 单独成型是为了让「说什么」可以被单测,而「怎么打印」留在 doctorAction 里。
type doctorFinding struct {
	Status string // ok | warn | info | hint
	Key    string
	Value  string
	Hint   string
}

// buildRuleReviewInput 把一份 config 摊成体检的原料。
//
// **两处容易读错的字段,都由测试钉着:**
//  1. global 取 cfg.Global(yaml `global:`)。cfg.Mode 是另一个东西,取值只有
//     host|router;spec 当天的真机 bug 就是拿错列表比,而拿错 mode 是同一形状。
//  2. 用户在 lists.china_domain 里换了自己的列表时,内嵌那份**不是**参照物 ——
//     那时不比,并说清为什么。拿错参照物会指着一条实际没被覆盖的规则说「可以删」。
func buildRuleReviewInput(cfg *config.Config, china []byte) rulereview.Input {
	in := rulereview.Input{GlobalProxy: cfg.Global}
	for _, r := range cfg.Rules {
		in.Direct = append(in.Direct, r.Direct...)
		in.Proxy = append(in.Proxy, r.Proxy...)
	}
	switch {
	case cfg.Lists.ChinaDomain != "":
		in.ChinaSkipReason = fmt.Sprintf("你在 lists.china_domain 指了自己的列表(%s),"+
			"内嵌那份不是参照物,这一类没有比对", cfg.Lists.ChinaDomain)
	case len(china) == 0:
		in.ChinaSkipReason = "拿不到内建 china 列表,这一类没有比对"
	default:
		in.China = route.NewDomainSet(chinaDomainPatterns(china))
	}
	return in
}

// chinaDomainPatterns 按 supervisor 侧 readLines 的同一规则拆行:去空白、跳注释与空行。
func chinaDomainPatterns(raw []byte) []string {
	var out []string
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out
}

// ruleReviewDoctorLines 把体检报告翻成 doctor 的行。
//
// **干净就一个字都不说。** 只在真有问题时才占地方,是这套东西不被训练成噪声的前提
// (与按规则失败计数同一条纪律)。唯一的例外是「没查」——那必须说,因为静默的
// 「没查」与「没问题」在用户眼里长得一模一样。
func ruleReviewDoctorLines(rep rulereview.Report) []doctorFinding {
	var out []doctorFinding

	for _, f := range rep.Findings {
		if f.Class != rulereview.ClassRisky {
			continue
		}
		// 安全结论,warn 而不是 info,并且点名到配置里那一行的原文。
		out = append(out, doctorFinding{
			Status: "warn",
			Key:    "risky direct rule",
			Value:  fmt.Sprintf("%s —— %s", f.Rule, f.Summary),
			Hint:   fmt.Sprintf("bx direct remove '%s'(改完要 bx down && bx up)", f.Rule),
		})
	}

	if n := rep.OverriddenCount; n > 0 {
		out = append(out, doctorFinding{
			Status: "warn",
			Key:    "rules never in effect",
			Value:  summarizeClass(rep, rulereview.ClassOverriddenByOppositeKind, n, "条 direct 规则被更宽的 proxy 规则压住,从来没生效过"),
		})
	}
	if n := rep.ShadowedByUserCount; n > 0 {
		out = append(out, doctorFinding{
			Status: "info",
			Key:    "redundant rules",
			Value:  summarizeClass(rep, rulereview.ClassShadowedByUserRule, n, "条被你自己更宽的一条覆盖,删掉不改变任何流量"),
		})
	}
	if rep.BuiltinListChecked {
		if n := rep.ShadowedByBuiltinCount; n > 0 {
			out = append(out, doctorFinding{
				Status: "info",
				Key:    "covered by builtin list",
				Value:  summarizeClass(rep, rulereview.ClassShadowedByBuiltinList, n, "条与内建 china 列表相关"),
			})
		}
	} else if rep.BuiltinSkipReason != "" {
		// **「没查」不许静默。** 它与「查了没有」在用户眼里长得一样,
		// 而两者的差别正是这个功能最贵的那个教训。
		out = append(out, doctorFinding{
			Status: "info",
			Key:    "builtin list check",
			Value:  "未检查:" + rep.BuiltinSkipReason,
		})
	}
	return out
}

// summarizeClass 打出「N 条 + 前三条点名」。全部列出来会把 doctor 淹掉,
// 一条不点名又等于没说 —— 折中是给数字 + 够他去配置里搜的那几条原文。
func summarizeClass(rep rulereview.Report, class rulereview.Class, n int, tail string) string {
	var names []string
	for _, f := range rep.Findings {
		if f.Class != class {
			continue
		}
		names = append(names, fmt.Sprintf("%s ← %s", f.Rule, f.CoveredBy))
		if len(names) == 3 {
			break
		}
	}
	s := fmt.Sprintf("%d %s:%s", n, tail, strings.Join(names, "、"))
	if n > len(names) {
		s += fmt.Sprintf(" 等 %d 条", n)
	}
	return s
}

// ruleReviewCheckName 把人话 key 换成 JSON 里稳定的 snake_case 名 ——
// agent 与 MCP 按名字取,名字变了就是接口变了。
func ruleReviewCheckName(key string) string {
	return "rule_" + strings.ReplaceAll(key, " ", "_")
}
