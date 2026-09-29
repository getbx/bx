package singboxrules

import (
	"net/netip"
	"strings"
)

// Destination 是一次评估的目的地:有域名按域名判,否则按 IP 判 —— 与 route.Meta 同构。
type Destination struct {
	Domain string
	IP     netip.Addr
}

// Verdict 是评估结果。RuleIndex 是命中的 route.rules 下标,-1 表示落到 final。
type Verdict struct {
	Outbound  string
	RuleIndex int
}

// Evaluate 按 sing-box 的语义评估一份 Bundle。**它是 sing-box 的参考模型,不是 bx 的
// 判据**:这里的每一条语义都在 spec §4 用真 sing-box 实测过(域名后缀按标签边界;首个
// 命中者胜;域名目的地不碰 ip_cidr,sing-box 不为它解析;rule_set 展开成文件里的规则),
// 并由 singbox_binary_test.go 拿内嵌的二进制复核。翻译得对不对,由 consistency_test.go
// 拿 route.Explain 与它逐输入比对来回答。
func (b Bundle) Evaluate(dst Destination) Verdict {
	dom := normalizeQuery(dst.Domain)
	for i, r := range b.Route.Rules {
		if b.ruleMatches(r, dom, dst.IP) {
			return Verdict{Outbound: r.Outbound, RuleIndex: i}
		}
	}
	return Verdict{Outbound: b.Route.Final, RuleIndex: -1}
}

func (b Bundle) ruleMatches(r Rule, dom string, ip netip.Addr) bool {
	if dom != "" {
		if suffixMatches(r.DomainSuffix, dom) {
			return true
		}
	} else if ip.IsValid() {
		if cidrMatches(r.IPCIDR, ip) {
			return true
		}
	} else {
		return false
	}
	for _, tag := range r.RuleSet {
		rs, ok := b.RuleSets[tag]
		if !ok {
			continue
		}
		for _, inner := range rs.Rules {
			if dom != "" && suffixMatches(inner.DomainSuffix, dom) {
				return true
			}
			if dom == "" && cidrMatches(inner.IPCIDR, ip) {
				return true
			}
		}
	}
	return false
}

// normalizeQuery:sing-box 交给路由的是小写、无尾点的域名;bx 的 DomainSet.Match 对查询
// 做的正是这两步。
func normalizeQuery(d string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(d), "."))
}

// suffixMatches:按标签边界(spec §4.1)。`a.com` 命中 `a.com` 与 `x.a.com`,不命中 `xa.com`。
func suffixMatches(suffixes []string, dom string) bool {
	for _, s := range suffixes {
		if dom == s || strings.HasSuffix(dom, "."+s) {
			return true
		}
	}
	return false
}

func cidrMatches(cidrs []string, ip netip.Addr) bool {
	for _, c := range cidrs {
		p, err := netip.ParsePrefix(c)
		if err != nil {
			continue
		}
		if p.Contains(ip) {
			return true
		}
	}
	return false
}
