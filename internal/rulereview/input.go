package rulereview

import (
	"net/netip"
	"strings"

	"github.com/getbx/bx/internal/route"
)

// Input 是一次体检的全部原料。
type Input struct {
	// Direct / Proxy 是用户 config 里 rules[].direct / rules[].proxy 的**原文**,
	// 由 setup.ListRules 摊平后传进来。
	Direct []string
	Proxy  []string

	// GlobalProxy 是 config 的 `global:` 开关。
	//
	// **不是 config.Mode。** config.Mode 取值只有 host|router,与 global/split 无关;
	// split|global|router|router-global 是 supervisor.proxyMode 派生的展示标签。
	// spec 写完当天的真机实测就栽在这:拿内建 china 列表比出 22 条「冗余」,
	// 而那台机器是 global —— 那 22 条全都在干活,照着删会让 22 个域名改走隧道。
	GlobalProxy bool

	// China 是内建 china 直连列表。nil = 没拿到(调用方没读到,或刻意不比)。
	// **nil 与「比了没命中」必须分得开**,由 ChinaSkipReason 说明。
	China *route.DomainSet
	// ChinaSkipReason 在 China 为 nil 或被 GlobalProxy 压制时给出人话理由。
	ChinaSkipReason string
}

// normalizeRule 把一条规则化成可比对的域名形式,与 route.NewDomainSet 的处理一致:
// 小写、去空白、去 `*.` 前缀、去尾点。
//
// **归一化只有这一份,而且只用于比对** —— 报给用户的一律是原文。
func normalizeRule(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimPrefix(s, "*.")
	return strings.TrimSuffix(s, ".")
}

// isNetworkLiteral 判断这一条是不是 CIDR 或裸 IP。
//
// rules[].direct / rules[].proxy 两种都收(supervisor.BuildRouter 的 asCIDR 会把
// 网络字面量分到 CIDRSet、其余当域名)。本包**不复制**那份判定 —— 判定只有一份是
// 本仓库的纪律 —— 只做一件事:认出网络字面量就整条跳过,一个字都不说。
//
// 代价是**沉默**(漏报),而不是对着一条 CIDR 断言「这条可以删」(错报)。
// 两者代价不对称。
func isNetworkLiteral(s string) bool {
	s = strings.TrimSpace(s)
	if _, err := netip.ParsePrefix(s); err == nil {
		return true
	}
	_, err := netip.ParseAddr(s)
	return err == nil
}

// domainRule 是一条通过筛选的域名规则:原文 + 归一化形式。
type domainRule struct {
	raw  string
	norm string
}

// domainRules 过滤出可判的域名规则,并**按归一化形式去重**。
//
// 去重是必须的,不是优化:两条完全重复的规则会互相指认对方是「更宽的那一条」,
// 报告于是说两条都可以删 —— 用户照做,规则整个没了。重复时保留**第一条原文**
// (与 route.NewDomainSet 对重复后缀的处置一致:报哪一条都对,但要稳定)。
func domainRules(list []string) []domainRule {
	seen := make(map[string]struct{}, len(list))
	out := make([]domainRule, 0, len(list))
	for _, raw := range list {
		if strings.TrimSpace(raw) == "" || isNetworkLiteral(raw) {
			continue
		}
		n := normalizeRule(raw)
		if n == "" {
			continue
		}
		if _, dup := seen[n]; dup {
			continue
		}
		seen[n] = struct{}{}
		out = append(out, domainRule{raw: raw, norm: n})
	}
	return out
}
