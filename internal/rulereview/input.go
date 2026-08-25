package rulereview

import (
	"net/netip"
	"strings"
	"time"

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

	// China 是拿来比对的 china 直连列表。nil = 没拿到(调用方没读到,或刻意不比)。
	// **nil 与「比了没命中」必须分得开**,由 ChinaSkipReason 说明。
	//
	// **调用方负责保证 China 就是「Core 实际会用的那份」或它的可信代用品** ——
	// 本包不知道也不该知道文件在哪、谁在读它,只认调用方摆在这里的内容 +
	// ChinaSource/ChinaFallback 里带的说明。
	China *route.DomainSet
	// ChinaSkipReason 在 China 为 nil 或被 GlobalProxy 压制时给出人话理由。
	ChinaSkipReason string
	// ChinaSource 说明 China 字段的内容来自哪里(如「Core 当前实际使用的 china
	// 列表(/var/lib/bx/china_domain.txt)」或「内嵌快照(回落)」),供报告点名
	// 用户可以核对。**只在 China != nil 时有意义**;由调用方(internal/cli)填,
	// 本包不产出、只透传进 Report。
	ChinaSource string
	// —— 死规则那一类的原料(2026-08-24)——
	//
	// **本包不 import stats**(纯度守卫只许依赖 route 与 policy),所以这里用
	// 本包自己的 RuleKey/RuleCounts,由 internal/cli 负责从 stats 那边转换。

	// History 是**跨重启累计**的按规则计数。
	//
	// **nil = 拿不到**(Core 没在跑 / 读不出 / schema 不认),那时死规则这一类是
	// **没查**而不是「零条」—— 两者的差别正是这个功能最贵的那个教训。
	History map[RuleKey]RuleCounts
	// HistoryUptime 是 Core **累计在跑**的时长(门槛一)。
	HistoryUptime time.Duration
	// HistoryDecisions 是全局累计判定数,含内建列表命中(门槛二)。
	HistoryDecisions int64
	// HistoryVersions 是这段累积跨过的 bx 版本数,报告要说出来让用户打折。
	HistoryVersions int
	// HistoryOverflowed:按规则跟踪的表满过 ⇒ 「没有条目」不等于「没命中」
	// ⇒ **整类没查**。这是这一类里最要紧的一道门。
	HistoryOverflowed bool
	// HistorySkipReason 在 History 为 nil 时说明为什么。
	HistorySkipReason string

	// ChinaFallback 标记 China 并非「Core 实际会用的那份」本身,而是读不到它之后
	// 回落的代用品(如内嵌快照)——即便回落合理、值得报,用户也必须能分清
	// 「查了、用的是实时数据」与「查了、用的是可能过期的快照」,不能靠猜。
	ChinaFallback bool
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

// RuleKey 与 stats 那边的 ruleKey 同形(判定层 + 规则原文),但**是本包自己的
// 类型** —— 纯判据包不许依赖 stats。转换由 internal/cli 做。
type RuleKey struct {
	// Source 是做出判定的那一层,取值与 route.Source.String() 一致
	// (user_direct / user_proxy / china_domain / …)。
	Source string
	// Rule 是配置里那一行的原文;内建列表命中时为空。
	Rule string
}

// RuleCounts 是一条规则的**累计**尝试与失败次数。
type RuleCounts struct {
	Attempts int64
	Failures int64
}
