// Package policy applies the narrow, safety-reviewed domain policy surface.
package policy

import (
	"fmt"
	"strings"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/route"
	"gopkg.in/yaml.v3"
)

// riskyDirectDomains 是「任何人都能在上面注册一个子域」的平台清单。
//
// 清单本身导出给守卫用(Swift 那份右键候选过滤要与它逐字相同,见
// internal/cli/macos_menu_hazard_test.go);判据一律走 DirectRuleHazard。
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
	for _, d := range req.Add {
		if norm(d) == "" {
			return nil, false, fmt.Errorf("domain is empty")
		}
		if req.Mode == "direct" && DirectRisk(d) && !req.AllowRisk {
			return nil, false, fmt.Errorf("direct policy for %q is risky; require allow_risk", d)
		}
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
	remove := map[string]bool{}
	for _, d := range req.Remove {
		remove[norm(d)] = true
	}
	add := map[string]bool{}
	for _, d := range req.Add {
		add[norm(d)] = true
	}
	changed := false
	removeFrom := func(elem *yaml.Node, field string, want map[string]bool) {
		seq := mapping(elem, field)
		if seq == nil || seq.Kind != yaml.SequenceNode {
			return
		}
		kept := seq.Content[:0]
		for _, item := range seq.Content {
			if want[norm(item.Value)] {
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
	for _, elem := range rules.Content {
		if seq := mapping(elem, req.Mode); seq != nil && seq.Kind == yaml.SequenceNode {
			for _, item := range seq.Content {
				existing[norm(item.Value)] = true
			}
		}
	}
	if len(req.Add) > 0 {
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
		for _, d := range req.Add {
			if !existing[norm(d)] {
				seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: strings.TrimSpace(d)})
				existing[norm(d)] = true
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
