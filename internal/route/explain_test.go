package route

import (
	"net/netip"
	"testing"
)

// **判定必须说得出是哪条规则做的。**
//
// 起因是一次真实排查:配置里有五条 `*.steamstatic.com` 一类的强制直连规则,
// 它们指向的那条直连路已经完全不通了,而 bx 只报「direct 26186」——
// 26186 条里有多少条是被规则逼出去的、又有多少条失败了,它一个字都答不上来。
// 用户看到的是「Steam 图片裂了」,于是去怀疑 bx 和 CDN,排查了很久。
//
// 归因的价值不在于列出规则(用户自己写的,他知道),而在于**点名该删哪一行**。
func TestExplainNamesTheRuleThatDecided(t *testing.T) {
	r := &Router{
		UserProxy:     NewDomainSet([]string{"*.forced-proxy.com"}),
		UserDirect:    NewDomainSet([]string{"*.steamstatic.com", "gsa.apple.com"}),
		ChinaDomain:   NewDomainSet([]string{"qq.com"}),
		PrivateDirect: mustCIDR(DefaultPrivateCIDRs),
	}
	for _, tc := range []struct {
		name    string
		domain  string
		wantDec Decision
		wantSrc Source
		// **原文,不是内部归一化后的形式。** 归因要点名的是 config 里那一行;
		// 报 `steamstatic.com` 而用户写的是 `'*.steamstatic.com'`,他会去搜一个搜不到的串。
		wantRule string
	}{
		{"用户强制直连,多级子域", "cdn.akamai.steamstatic.com", Direct, SourceUserDirect, "*.steamstatic.com"},
		{"用户强制直连,精确域名", "gsa.apple.com", Direct, SourceUserDirect, "gsa.apple.com"},
		{"用户强制代理压过一切", "a.forced-proxy.com", Proxy, SourceUserProxy, "*.forced-proxy.com"},
		{"china 列表", "www.qq.com", Direct, SourceChinaDomain, ""},
		{"谁都没命中", "example.org", Proxy, SourceDefault, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dec, why := r.Explain(Meta{Domain: tc.domain})
			if dec != tc.wantDec || why.Source != tc.wantSrc || why.Rule != tc.wantRule {
				t.Fatalf("Explain(%q) = %v/%v/%q,want %v/%v/%q",
					tc.domain, dec, why.Source, why.Rule, tc.wantDec, tc.wantSrc, tc.wantRule)
			}
		})
	}
}

// **Decide 与 Explain 不许是两份判据。** 本仓库栽过多次的形状:同一个问题两个
// 实现,一个改了另一个没改,而两边的测试都绿。Decide 必须是 Explain 的薄壳。
func TestDecideAgreesWithExplainOnEveryInput(t *testing.T) {
	r := &Router{
		UserProxy:     NewDomainSet([]string{"*.p.com"}),
		UserDirect:    NewDomainSet([]string{"*.d.com"}),
		UserDirectIP:  mustCIDR([]string{"203.0.113.0/24"}),
		ChinaDomain:   NewDomainSet([]string{"cn.com"}),
		ChinaCIDR:     mustCIDR([]string{"1.2.3.0/24"}),
		PrivateDirect: mustCIDR(DefaultPrivateCIDRs),
	}
	for _, global := range []bool{false, true} {
		r.GlobalProxy = global
		for _, m := range []Meta{
			{Domain: "x.p.com"},
			{Domain: "x.d.com"},
			{Domain: "x.cn.com"},
			{Domain: "x.org"},
			{IP: mustAddr("203.0.113.9")},
			{IP: mustAddr("1.2.3.4")},
			{IP: mustAddr("10.0.0.1")},
			{IP: mustAddr("8.8.8.8")},
			{},
		} {
			want, _ := r.Explain(m)
			if got := r.Decide(m); got != want {
				t.Errorf("global=%v Decide(%+v)=%v 而 Explain 说 %v —— 两份判据分叉了", global, m, got, want)
			}
		}
	}
}

// IP 侧同样要归因:用户可能用 `direct` 的网段规则把一整段逼成直连。
func TestExplainIPNamesTheRule(t *testing.T) {
	r := &Router{
		UserDirectIP:  mustCIDR([]string{"203.0.113.0/24"}),
		PrivateDirect: mustCIDR(DefaultPrivateCIDRs),
		ChinaCIDR:     mustCIDR([]string{"1.2.3.0/24"}),
	}
	for _, tc := range []struct {
		ip  string
		dec Decision
		src Source
	}{
		{"203.0.113.9", Direct, SourceUserDirectIP},
		{"10.0.0.1", Direct, SourcePrivate},
		{"1.2.3.4", Direct, SourceChinaCIDR},
		{"8.8.8.8", Proxy, SourceDefault},
	} {
		dec, why := r.ExplainIP(mustAddr(tc.ip))
		if dec != tc.dec || why.Source != tc.src {
			t.Errorf("ExplainIP(%s) = %v/%v,want %v/%v", tc.ip, dec, why.Source, tc.dec, tc.src)
		}
	}
}

func mustAddr(s string) netip.Addr { return netip.MustParseAddr(s) }

func mustCIDR(cidrs []string) *CIDRSet {
	set, err := NewCIDRSet(cidrs)
	if err != nil {
		panic(err)
	}
	return set
}
