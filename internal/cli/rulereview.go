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

// chinaDomainPatterns 按行拆开内嵌 china 列表。**这里不是照抄 supervisor 的
// readLines**(它其实什么都不过滤,只是原样按行切开)——真正的先例是
// route.NewDomainSet:它自己也会 TrimSpace/跳注释/跳空行/去 `*.` 前缀。这里提前做
// 一遍是为了让这个函数本身可读、可单测(看得出「一行一条,# 是注释」这条约定),
// 与 NewDomainSet 内部那份重复但无害。
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
//
// **文本路径与 JSON 路径共用这一份判据** —— doctorAction 与 collectClientDoctorWith
// 都只是拿这个函数的返回值分别渲染,不许为了保住某一侧的呈现分叉成两条判断逻辑。
func ruleReviewDoctorLines(rep rulereview.Report) []doctorFinding {
	var out []doctorFinding

	if f := riskyRuleFinding(rep); f != nil {
		out = append(out, *f)
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

// riskyRuleFinding 把全部危险直连 finding 合并成**恰好一条** doctorFinding。
//
// **不是每条 finding 一行** —— policy.DirectRisk 的名单有 19 个域
// (aliyuncs/myqcloud/amazonaws/cloudfront/github.io…),配置里同时有两条危险直连
// 完全现实。ruleReviewCheckName 的整个存在理由是「agent 与 MCP 按名字取」,如果
// 每条 finding 各产出一条同名 check,--json 路径会对 rep.addCheck 同一个名字调
// 多次,产生多个同名 checkReport —— 按名字取的消费方(这是 JSON 路径唯一的读者)
// 只会拿到其中一条,静默丢掉其余的安全结论。一个去匿名化风险被静默丢掉,
// 方向正好是这个功能要防的那个错误的反面。
//
// **详情不截断,不同于 summarizeClass 的「前三条 + 等 N 条」**——那是给冗余/覆盖
// 这类「建议」用的折中(全列出来会把 doctor 淹掉);这一类是**安全结论**,少报一条
// 等于没报那一条。hint 同理:必须能一次处理全部规则,不能只给第一条的命令——
// 那会让用户以为删掉那一条就完了。
func riskyRuleFinding(rep rulereview.Report) *doctorFinding {
	var rules []string
	summary := ""
	for _, f := range rep.Findings {
		if f.Class != rulereview.ClassRisky {
			continue
		}
		rules = append(rules, f.Rule)
		if summary == "" {
			summary = f.Summary
		}
	}
	if len(rules) == 0 {
		return nil
	}
	quoted := make([]string, len(rules))
	for i, r := range rules {
		quoted[i] = "'" + r + "'"
	}
	return &doctorFinding{
		Status: "warn",
		Key:    "risky direct rule",
		Value: fmt.Sprintf("%d 条直连规则命中危险名单:%s —— %s",
			len(rules), strings.Join(rules, "、"), summary),
		// bx direct rm(不是 remove —— 那是这条 hint 上一版的笔误,命令本身
		// 不存在)接受多个域名一次处理,一条命令覆盖全部规则。
		Hint: fmt.Sprintf("bx direct rm %s(改完要 bx down && bx up)", strings.Join(quoted, " ")),
	}
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
