package singboxrules

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strings"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/policy"
	"github.com/getbx/bx/internal/route"
)

// Lists 是 china 两张列表的原文行(与 supervisor.BuildRouter 吃的同一形状):
// 调用方负责读文件,这里只翻译。
type Lists struct {
	ChinaDomain []string
	ChinaCIDR   []string
}

// Rule 是 sing-box route.rules 里的一条(只用到 bx 需要的四个键)。
type Rule struct {
	DomainSuffix []string `json:"domain_suffix,omitempty"`
	IPCIDR       []string `json:"ip_cidr,omitempty"`
	RuleSet      []string `json:"rule_set,omitempty"`
	Outbound     string   `json:"outbound,omitempty"`
}

// RuleSetRef 是 route.rule_set 里的一条声明:本地 source 格式文件,路径就是 <tag>.json,
// 由调用方把 RuleSetJSON(tag) 写到 libbox 工作目录下的同名文件。
type RuleSetRef struct {
	Tag    string `json:"tag"`
	Type   string `json:"type"`
	Format string `json:"format"`
	Path   string `json:"path"`
}

// Route 对应 sing-box 配置的 route 段。
type Route struct {
	Rules   []Rule       `json:"rules"`
	RuleSet []RuleSetRef `json:"rule_set,omitempty"`
	Final   string       `json:"final"`
}

// RuleSetFile 是 source 格式的 rule-set 文件({"version":1,"rules":[…]}),rules 里只有
// domain_suffix 或 ip_cidr,没有 outbound。
type RuleSetFile struct {
	Version int    `json:"version"`
	Rules   []Rule `json:"rules"`
}

// Bundle 是一次翻译的全部产物。
type Bundle struct {
	Route    Route
	RuleSets map[string]RuleSetFile
}

// ErrUnsupportedEgress:配置用了具名出口(egress[] / rules[].via)。手机上没有对应物,
// **拒绝而不是丢掉** —— 丢掉就是「同一地址两边走法不同而两边都不报错」。
var ErrUnsupportedEgress = errors.New("named egress (egress[] / rules[].via) has no equivalent on the phone")

// Translate 把 cfg 的分流意图翻成 sing-box 规则。顺序逐条照 route.Explain:
//
//	域名:user proxy → user direct → china domain(global 时省略)→ final proxy
//	IP:  user proxy cidr → user direct cidr → 私网 → china cidr(global 时省略)→ final proxy
//
// 域名目的地在 sing-box 里不会去碰 ip_cidr 规则(不解析),所以两族规则在一个数组里
// 前后并排不会互相影响 —— 这与 bx「有域名就不走 IP 路」同构。
func Translate(cfg *config.Config, lists Lists) (Bundle, error) {
	if cfg == nil {
		return Bundle{}, errors.New("nil config")
	}
	if len(cfg.Egress) > 0 {
		return Bundle{}, fmt.Errorf("%w: egress has %d entries", ErrUnsupportedEgress, len(cfg.Egress))
	}
	var proxyDoms, directDoms, proxyCIDRs, directCIDRs []string
	for _, rule := range cfg.Rules {
		if strings.TrimSpace(rule.Via) != "" || len(rule.CIDR) > 0 {
			return Bundle{}, fmt.Errorf("%w: rules[] entry with via=%q", ErrUnsupportedEgress, rule.Via)
		}
		splitRules(rule.Proxy, &proxyDoms, &proxyCIDRs)
		splitRules(rule.Direct, &directDoms, &directCIDRs)
	}

	var rules []Rule
	add := func(r Rule) {
		if len(r.DomainSuffix)+len(r.IPCIDR)+len(r.RuleSet) == 0 {
			return // sing-box 对空 rule 报错;bx 里空列表就是「没有这层」
		}
		rules = append(rules, r)
	}
	add(Rule{DomainSuffix: proxyDoms, Outbound: OutboundProxy})
	add(Rule{DomainSuffix: directDoms, Outbound: OutboundDirect})
	if !cfg.Global {
		add(Rule{RuleSet: []string{RuleSetChinaDomain}, Outbound: OutboundDirect})
	}
	add(Rule{IPCIDR: proxyCIDRs, Outbound: OutboundProxy})
	add(Rule{IPCIDR: directCIDRs, Outbound: OutboundDirect})
	add(Rule{IPCIDR: append([]string(nil), route.DefaultPrivateCIDRs...), Outbound: OutboundDirect})
	if !cfg.Global {
		add(Rule{RuleSet: []string{RuleSetChinaCIDR}, Outbound: OutboundDirect})
	}

	b := Bundle{
		Route: Route{
			Rules: rules,
			RuleSet: []RuleSetRef{
				{Tag: RuleSetChinaDomain, Type: "local", Format: "source", Path: RuleSetChinaDomain + ".json"},
				{Tag: RuleSetChinaCIDR, Type: "local", Format: "source", Path: RuleSetChinaCIDR + ".json"},
			},
			Final: OutboundProxy,
		},
		RuleSets: map[string]RuleSetFile{
			RuleSetChinaDomain: {Version: 1, Rules: []Rule{{DomainSuffix: normalizeSuffixes(lists.ChinaDomain)}}},
			RuleSetChinaCIDR:   {Version: 1, Rules: []Rule{{IPCIDR: normalizeCIDRs(lists.ChinaCIDR)}}},
		},
	}
	return b, nil
}

// splitRules 把一条规则列表按 policy.RuleCIDR 分成域名后缀与网段 —— 与 supervisor.BuildRouter
// 同一个判定。域名走与 route.NewDomainSet 同一条归一化。
func splitRules(entries []string, doms, cidrs *[]string) {
	for _, e := range entries {
		if p, ok := policy.RuleCIDR(e); ok {
			*cidrs = append(*cidrs, p.String())
			continue
		}
		if s, ok := normalizeSuffix(e); ok {
			*doms = append(*doms, s)
		}
	}
}

// normalizeSuffix 与 route.NewDomainSet 逐字同条:去空白、小写、去 `*.`,空行与 `#` 跳过。
// 漂了的后果由 consistency_test.go 抓(同一份配置两边判定不同)。
func normalizeSuffix(pattern string) (string, bool) {
	p := strings.ToLower(strings.TrimSpace(pattern))
	p = strings.TrimPrefix(p, "*.")
	if p == "" || strings.HasPrefix(p, "#") {
		return "", false
	}
	return p, true
}

func normalizeSuffixes(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if s, ok := normalizeSuffix(l); ok {
			out = append(out, s)
		}
	}
	return out
}

// normalizeCIDRs 与 route.NewCIDRSet 逐字同条:去空白,跳过空行、`#` 与**解析不了的行**
// (NewCIDRSet 对坏行是容错跳过)。两边都跳过,坏行才不会成为手机与桌面的差异;
// 带出去则一行坏数据会让整份手机配置在 check 那一步整个失败。
func normalizeCIDRs(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		p, err := netip.ParsePrefix(l)
		if err != nil {
			continue
		}
		out = append(out, p.String())
	}
	return out
}

// RouteJSON 是 {"route": …},可直接 merge 进 libbox 配置。
func (b Bundle) RouteJSON() ([]byte, error) {
	return json.Marshal(struct {
		Route Route `json:"route"`
	}{b.Route})
}

// RuleSetJSON 是 tag 对应的 source 格式 rule-set 文件内容。
func (b Bundle) RuleSetJSON(tag string) ([]byte, error) {
	rs, ok := b.RuleSets[tag]
	if !ok {
		return nil, fmt.Errorf("no rule-set %q in this bundle", tag)
	}
	return json.Marshal(rs)
}
