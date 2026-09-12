package policy

import (
	"fmt"
	"net/netip"
	"regexp"
	"strings"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/route"
)

// 域名/通配符的形状。允许 `*.` 前缀(bx 的 DomainSet 认它),其余必须是普通域名标签。
//
// **在写盘之前挡掉非法输入**:一条带空格或换行的「域名」进了 config,要等到
// 下一次 bx up 才会发现 —— 而那时用户已经断过一次网了。
var domainPattern = regexp.MustCompile(`^(\*\.)?([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)+[a-z]{2,}$`)

// RulePattern 是一条 direct/proxy 规则**校验并归一化之后**的样子。
//
// Key 是**覆盖键**:两条规则的 Key 相同 ⇒ 它们盖住的东西一模一样。这个仓库的
// 匹配器是后缀集(route.NewDomainSet 去掉 `*.` 只存后缀),于是 `zoom.us` 与
// `*.zoom.us` 是**同一条规则的两种写法** —— 按字面串比对会让「加进一边就从
// 另一边删掉」这条不变量对换了写法的同一条规则整个失效。
type RulePattern struct {
	Text string // 写进 config 的那一行
	Key  string // 覆盖键
	CIDR bool   // 网段(含裸 IP 补成 /32、/128),不是域名
}

// RuleCIDR 把一条规则条目识别成网段:已是 CIDR 的原样解析;裸 IP 补成 /32 或
// /128;域名模式返回 ok=false。
//
// **判定只有一份**:supervisor.BuildRouter 拿它把 rules 分流到 CIDRSet 还是
// DomainSet,写入路径拿它决定「这条要不要按域名校验」。两处各写一份的后果是
// 静默的:写入侧当域名拒了一条 BuildRouter 本来会当网段接受的规则(菜单右键
// 给出的 IP 候选正是这种),或者反过来放进去一条谁都不认的东西。
func RuleCIDR(s string) (netip.Prefix, bool) {
	s = strings.TrimSpace(s)
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return netip.Prefix{}, false
		}
		return p, true
	}
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, false
	}
	return netip.PrefixFrom(addr, addr.BitLen()), true
}

// ParseRulePattern 校验一条规则的写法并归一化。
//
// **归一化走 config.NormalizeHostName**(小写、去空白、去尾点),与 hosts 覆盖
// 那一侧同一个函数:两侧各写一份归一化时,`Torchfun.com.` 与 `torchfun.com` 会
// 静默并存 —— 那次是两个 Critical,`NormalizeHostName` 就是为此存在的。
//
// 最后一道门拿**生产那份匹配器**验一遍。**今天它不可达**:归一化去掉尾点、
// 正则又只放行普通标签,走到这里的串 route.NewDomainSet 一定认得。写下来是因为
// 它盯的不是这条正则,是**匹配器与写入侧的耦合** —— 谁哪天放宽了正则(尾点、
// IDN、下划线……),这一句会当场把那条永远不会命中的规则挡回去,而不是让它以
// 一条「在 `bx direct ls` 和菜单里长得完全正常」的死规则的样子躺进配置
// (`example.com.` 正是这种:Match 去掉的是**查询**的尾点,不是**模式**的)。
// 相应地,它没有变异测试能咬中 —— 那份性质由 writepath_test.go 里
// 「写进去的东西匹配器必须真的匹配得上」经生产 Router 钉住。
func ParseRulePattern(raw string) (RulePattern, error) {
	if p, ok := RuleCIDR(raw); ok {
		text := p.Addr().String()
		if strings.Contains(strings.TrimSpace(raw), "/") {
			text = p.String()
		}
		return RulePattern{Text: text, Key: p.Masked().String(), CIDR: true}, nil
	}
	name := config.NormalizeHostName(raw)
	if name == "" {
		return RulePattern{}, fmt.Errorf("rule pattern is empty")
	}
	if strings.ContainsAny(name, " \t\n\r'\"") {
		return RulePattern{}, fmt.Errorf("rule %q contains whitespace or quotes — a domain has neither", raw)
	}
	if !domainPattern.MatchString(name) {
		return RulePattern{}, fmt.Errorf("rule %q is neither a domain, a *.domain, nor an IP/CIDR", raw)
	}
	key := strings.TrimPrefix(name, "*.")
	if !route.NewDomainSet([]string{name}).Match(key) {
		return RulePattern{}, fmt.Errorf("rule %q would never match anything — bx's matcher does not recognize it", raw)
	}
	return RulePattern{Text: name, Key: key}, nil
}

// ValidateRulePattern 校验一条规则并返回归一化后要写进配置的那一行。
//
// setup.AddRule / RemoveRule / ApplyGroup 也走它 —— 两条写入路径共用一个校验器,
// 不是两份。
func ValidateRulePattern(pattern string) (string, error) {
	p, err := ParseRulePattern(pattern)
	if err != nil {
		return "", err
	}
	return p.Text, nil
}

// CoverageKey 是**已经躺在配置里**那一行的覆盖键。
//
// 它刻意宽容、不校验:盘上可能有这次修复之前写进去的畸形规则(`example.com.`),
// 而用户必须删得掉它们 —— 一个「删的时候也要先通过校验」的实现会把它们永久
// 锁在配置里,而那正是本次要消灭的东西。
func CoverageKey(raw string) string {
	if p, ok := RuleCIDR(raw); ok {
		return p.Masked().String()
	}
	return strings.TrimPrefix(config.NormalizeHostName(raw), "*.")
}

// coveringRule 在 existing 里找一条**严格更宽**、把 want 整个盖住的规则,返回
// 配置里那一行的原文。
//
// 覆盖**相等**的不算(它们由「加进一边就从另一边删掉」处理);这里只回答
// 「有没有一条更宽的压在上面」。
func coveringRule(existing []string, want RulePattern) (string, bool) {
	if want.CIDR {
		mine, ok := RuleCIDR(want.Text)
		if !ok {
			return "", false
		}
		for _, raw := range existing {
			other, ok := RuleCIDR(raw)
			if !ok || other.Bits() >= mine.Bits() {
				continue // 一样宽或更窄:盖不住
			}
			if other.Contains(mine.Addr()) {
				return strings.TrimSpace(raw), true
			}
		}
		return "", false
	}
	var wider []string
	for _, raw := range existing {
		if CoverageKey(raw) == want.Key {
			continue // 覆盖相等 —— 那是同一条规则的另一种写法,不是「更宽」
		}
		if _, isCIDR := RuleCIDR(raw); isCIDR {
			continue
		}
		wider = append(wider, raw)
	}
	if len(wider) == 0 {
		return "", false
	}
	return route.NewDomainSet(wider).MatchRule(want.Key)
}
