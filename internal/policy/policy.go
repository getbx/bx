// Package policy applies the narrow, safety-reviewed domain policy surface.
package policy

import (
	"errors"
	"fmt"
	"strings"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/route"
	"gopkg.in/yaml.v3"
)

// riskyDirectDomains 是「任何人都能在上面注册一个子域」的平台清单。
//
// **清单本身不导出**:守卫拿不到符号,它是从**本文件的源码文本**里用正则抠下面
// 那个变量声明块的字面量,再与 Swift 那份右键候选过滤逐字比对
// (internal/cli/macos_menu_hazard_test.go)—— 改名或换写法会让它抠不出来,那时它
// t.Fatal 响亮失败而不是静默放行。判据一律走 DirectRuleHazard。
//
// **别在本文件的注释里复写那句声明**:那条正则是非贪婪的,注释里出现一次就会被
// 先匹配上、抠出一份空清单,守卫当场转红(2026-09-12 实测踩过一次)。
var riskyDirectDomains = []string{
	"aliyuncs.com", "myqcloud.com", "bcebos.com", "qiniucdn.com", "qbox.me", "clouddn.com", "upaiyun.com", "myhuaweicloud.com",
	"amazonaws.com", "cloudfront.net", "core.windows.net", "googleapis.com", "r2.dev", "workers.dev", "pages.dev", "github.io", "vercel.app", "netlify.app", "b-cdn.net",
}

var riskyDirect = route.NewDomainSet(riskyDirectDomains)

type Request struct {
	Mode      string
	Add       []string
	Remove    []string
	AllowRisk bool
}

func norm(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// DirectRuleHazard 判一条 direct 规则会不会打开一个去匿名化的洞,并给出
// 用户看得懂的理由与出路。**判据是「这条规则覆盖到哪里」,不是「它写成什么样」。**
//
// bx 的匹配器是**后缀集**:route.NewDomainSet 去掉 `*.` 只存后缀,Match 逐级
// 往父域找(internal/route/domainset.go)。于是这个代码库里**根本没有「确切主机
// 规则」这种东西** —— `bucket.s3.amazonaws.com` 与 `*.bucket.s3.amazonaws.com`
// 覆盖的子树一模一样,`evil.bucket.s3.amazonaws.com` 两种写法都命中。只拦带
// `*.` 的那一种,等于这道门在**裸写**的形式上完全不设防,而攻击者要的那个子域
// 在两种写法下都躺在直连白名单里。
//
// 好用由**逃生口**买单,不由放松判据买单:CLI 的 `--force`、菜单被 409 拒绝
// 之后的「Add Anyway」。
//
// reason/suggestion 是**英文**:`bx direct add` 直接打印它们,而 CLI 这一路的
// 提示与菜单的用户可见字符串保持同一种语言。Guardian 只把它们写进自己的日志,
// 响应体按纪律只回失败码(菜单那句话是 riskyDirectRuleWarning,同义不同字)。
func DirectRuleHazard(pattern string) (hazard bool, reason, suggestion string) {
	p := strings.TrimPrefix(strings.TrimSuffix(norm(pattern), "."), "*.")
	if p == "" || !riskyDirect.Match(p) {
		return false, "", ""
	}
	return true,
		"Anyone can register a subdomain on this platform, and a bx direct rule covers every subdomain of what you write — so a stranger could make your real IP leave outside the tunnel.",
		"Naming the exact host you use narrows the exposure but does not remove it, because that rule still covers its subdomains. Add --force if you really control that host."
}

// DirectRisk reports whether a direct rule would cover a public cloud or
// open-subdomain platform that an unrelated party could use for de-anonymizing
// traffic.
//
// **薄壳,判定只有一份。** 两个判据会让同一个域名在一处被拦、在另一处被放行,
// 而用户无从分辨谁对 —— 这个仓库为这个形状栽过。
func DirectRisk(domain string) bool {
	hazard, _, _ := DirectRuleHazard(domain)
	return hazard
}

func mapping(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

func removeMapping(n *yaml.Node, key string) bool {
	if n == nil || n.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			n.Content = append(n.Content[:i], n.Content[i+2:]...)
			return true
		}
	}
	return false
}

// Apply validates and edits rules without rewriting unrelated YAML. Adding one
// mode removes the same domains from the opposite mode, preventing ambiguity.
func Apply(in []byte, req Request) ([]byte, bool, error) {
	return apply(in, req, true)
}

// Edit provides the same canonical YAML edit semantics to trusted local CLI
// callers that already validate their surrounding configuration and risk gate.
func Edit(in []byte, req Request) ([]byte, bool, error) {
	return apply(in, req, false)
}

func apply(in []byte, req Request, validateConfig bool) ([]byte, bool, error) {
	if req.Mode != "direct" && req.Mode != "proxy" {
		return nil, false, fmt.Errorf("mode must be direct or proxy")
	}
	if len(req.Add) == 0 && len(req.Remove) == 0 {
		return nil, false, fmt.Errorf("add or remove is required")
	}
	if validateConfig {
		if _, err := config.Parse(in); err != nil {
			return nil, false, err
		}
	}
	// **校验在动 YAML 之前全部做完。** 这条路(bx direct/proxy add、MCP 的
	// bx_policy_apply)此前一个字都不校验,于是 `example.com.` 这种谁都匹配不上
	// 的规则写得进去、`bx direct ls` 与菜单里还显示成一条正常规则。
	adds := make([]RulePattern, 0, len(req.Add))
	for _, d := range req.Add {
		p, err := ParseRulePattern(d)
		if err != nil {
			return nil, false, err
		}
		if req.Mode == "direct" && DirectRisk(p.Text) && !req.AllowRisk {
			return nil, false, fmt.Errorf("direct policy for %q is risky; require allow_risk", d)
		}
		adds = append(adds, p)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(in, &doc); err != nil || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, false, fmt.Errorf("invalid YAML config")
	}
	root := doc.Content[0]
	rules := mapping(root, "rules")
	if rules == nil {
		rules = &yaml.Node{Kind: yaml.SequenceNode}
		root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: "rules"}, rules)
	}
	if rules.Kind != yaml.SequenceNode {
		return nil, false, fmt.Errorf("rules must be a sequence")
	}
	opp := "direct"
	if req.Mode == "direct" {
		opp = "proxy"
	}
	if err := refuseRulesTheOppositeModeAlreadyCovers(fieldValues(rules, opp), req.Mode, adds); err != nil {
		return nil, false, err
	}
	remove := map[string]bool{}
	for _, d := range req.Remove {
		// 删除**不过校验**,只取覆盖键:盘上可能躺着这次修复之前写进去的畸形
		// 规则,而那正是用户最需要删掉的东西。覆盖键还让 `rm example.com` 删得掉
		// 写成 `*.example.com` 的那一行 —— 两种写法盖住的东西一模一样,只按字面
		// 串比对会如实报「不在列表里」,而它明明就在。
		remove[CoverageKey(d)] = true
	}
	add := map[string]bool{}
	for _, p := range adds {
		add[p.Key] = true
	}
	changed := false
	removeFrom := func(elem *yaml.Node, field string, want map[string]bool) {
		seq := mapping(elem, field)
		if seq == nil || seq.Kind != yaml.SequenceNode {
			return
		}
		kept := seq.Content[:0]
		for _, item := range seq.Content {
			if want[CoverageKey(item.Value)] {
				changed = true
				continue
			}
			kept = append(kept, item)
		}
		seq.Content = kept
		if len(seq.Content) == 0 {
			removeMapping(elem, field)
		}
	}
	for _, elem := range rules.Content {
		if elem != nil && elem.Kind == yaml.MappingNode {
			removeFrom(elem, req.Mode, remove)
			removeFrom(elem, opp, add)
		}
	}
	keptRules := rules.Content[:0]
	for _, elem := range rules.Content {
		if elem != nil && elem.Kind == yaml.MappingNode && len(elem.Content) == 0 {
			continue
		}
		keptRules = append(keptRules, elem)
	}
	rules.Content = keptRules
	existing := map[string]bool{}
	for _, v := range fieldValues(rules, req.Mode) {
		existing[CoverageKey(v)] = true
	}
	if len(adds) > 0 {
		if len(rules.Content) == 0 {
			rules.Content = append(rules.Content, &yaml.Node{Kind: yaml.MappingNode})
		}
		first := rules.Content[0]
		if first.Kind != yaml.MappingNode {
			return nil, false, fmt.Errorf("rules entries must be mappings")
		}
		seq := mapping(first, req.Mode)
		if seq == nil {
			seq = &yaml.Node{Kind: yaml.SequenceNode}
			first.Content = append(first.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: req.Mode}, seq)
		}
		if seq.Kind != yaml.SequenceNode {
			return nil, false, fmt.Errorf("%s must be a sequence", req.Mode)
		}
		for _, p := range adds {
			if !existing[p.Key] {
				seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: p.Text})
				existing[p.Key] = true
				changed = true
			}
		}
	}
	if !changed {
		return in, false, nil
	}
	out, err := yaml.Marshal(&doc)
	if err != nil {
		return nil, false, err
	}
	if validateConfig {
		if _, err := config.Parse(out); err != nil {
			return nil, false, fmt.Errorf("edited config is invalid: %w", err)
		}
	}
	return out, true, nil
}

// fieldValues 收集 rules 里某一侧(direct/proxy)全部条目的原文。
//
// 扫**所有** rules 元素:域名可能写在 rules[1] 上,漏看一个的后果是判定按一份
// 残缺的现状做出来的。
func fieldValues(rules *yaml.Node, field string) []string {
	var out []string
	for _, elem := range rules.Content {
		seq := mapping(elem, field)
		if seq == nil || seq.Kind != yaml.SequenceNode {
			continue
		}
		for _, item := range seq.Content {
			out = append(out, item.Value)
		}
	}
	return out
}

// ErrCoveredByOppositeMode 是「这条规则写下去也永远不会命中」这一类拒绝。
//
// 导出成哨兵而不是让调用方去 strings.Contains 错误文本:MCP 那一侧要按它给出
// 对得上的处置建议,而按文本认的话,哪天措辞改一个词,建议就悄悄退回一句通用的
// 废话,两边都不会报错。
var ErrCoveredByOppositeMode = errors.New("rule is already covered by the opposite mode")

// refuseRulesTheOppositeModeAlreadyCovers 拦下一条**写下去也永远不会命中**的
// direct 规则。
//
// route.Router.Explain 先查 UserProxy 再查 UserDirect,**没有「更具体优先」这回
// 事**。所以 proxy 里有 `*.example.com` 时,往 direct 加 `a.example.com` 的结果
// 是:文件变了、CLI 打勾、MCP 回 changed=true,而那条规则一次都不会生效。
// rulereview 已经能事后认出这个形状(ClassOverriddenByOppositeKind),但那是
// 体检报告 —— 写入路径明知会造出一条死规则还报成功,是这个仓库明令禁止的那种谎。
//
// **为什么这道门没有 --force。** 风险名单那道门可以判错(用户可能真的独占那台
// 主机),所以它必须留逃生口;这一道判的不是风险,是 Explain 的查找顺序 ——
// 它不会错,而放行的唯一结果是往配置里种一条死规则。**能判错的门要逃生口,
// 不能判错的门不要**:给它一个 --force,等于给「明知没有效果」发一张通行证。
// 出路是真出路:先把那条更宽的 proxy 规则删掉或收窄,消息里直接把命令给出来。
//
// **只对 direct 这一侧成立,反过来不是。** proxy 排在前面,所以 direct 有
// `*.example.com` 时往 proxy 加 `a.example.com` 是一条**正在生效的例外**(把
// 一小块流量拉回隧道),不是死规则 —— 拦它就是拦掉一个合法而且常用的写法。
// 同一张表里加一条更窄的也不拦:判定结果与用户要的一致,他只是多写了一行。
func refuseRulesTheOppositeModeAlreadyCovers(oppositeEntries []string, mode string, adds []RulePattern) error {
	if mode != "direct" || len(oppositeEntries) == 0 {
		return nil
	}
	for _, p := range adds {
		rule, ok := coveringRule(oppositeEntries, p)
		if !ok {
			continue
		}
		return fmt.Errorf("%w: direct rule %q can never take effect because the proxy rule %q already covers it, and bx consults proxy rules before direct ones (there is no most-specific-wins). Remove or narrow that proxy rule first: bx proxy rm %s",
			ErrCoveredByOppositeMode, p.Text, rule, rule)
	}
	return nil
}
