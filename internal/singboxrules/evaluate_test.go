package singboxrules

import (
	"net/netip"
	"testing"
)

func bundle(rules []Rule, sets map[string]RuleSetFile) Bundle {
	return Bundle{Route: Route{Rules: rules, Final: OutboundProxy}, RuleSets: sets}
}

func domain(d string) Destination { return Destination{Domain: d} }
func ip(s string) Destination     { return Destination{IP: netip.MustParseAddr(s)} }

// spec §4.1 实测:domain_suffix 按标签边界匹配,不是裸字符串后缀。
func TestEvaluateDomainSuffixMatchesOnLabelBoundaries(t *testing.T) {
	b := bundle([]Rule{{DomainSuffix: []string{"a.com"}, Outbound: OutboundDirect}}, nil)
	cases := map[string]string{
		"a.com":     OutboundDirect,
		"x.a.com":   OutboundDirect,
		"y.x.a.com": OutboundDirect,
		"evila.com": OutboundProxy, // final
		"xa.com":    OutboundProxy,
		"A.COM.":    OutboundDirect, // 大写与尾点:sing-box 拿到的是归一化后的域名,bx 的 Match 也归一化
	}
	for d, want := range cases {
		if got := b.Evaluate(domain(d)); got.Outbound != want {
			t.Errorf("%s → %s (rule %d), want %s", d, got.Outbound, got.RuleIndex, want)
		}
	}
}

// spec §4.2 实测:首个命中者胜;没命中落 final,RuleIndex = -1。
func TestEvaluateFirstMatchWinsAndFinalIsMinusOne(t *testing.T) {
	b := bundle([]Rule{
		{DomainSuffix: []string{"zoom.us"}, Outbound: OutboundProxy},
		{DomainSuffix: []string{"zoom.us"}, Outbound: OutboundDirect},
	}, nil)
	if got := b.Evaluate(domain("us04web.zoom.us")); got.Outbound != OutboundProxy || got.RuleIndex != 0 {
		t.Fatalf("got %+v, want proxy by rule 0", got)
	}
	if got := b.Evaluate(domain("example.org")); got.Outbound != OutboundProxy || got.RuleIndex != -1 {
		t.Fatalf("got %+v, want final (-1)", got)
	}
}

// 域名目的地不碰 ip_cidr(sing-box 不为 ip_cidr 解析域名);IP 目的地不碰 domain_suffix。
func TestEvaluateKeepsDomainAndIPFamiliesApart(t *testing.T) {
	b := bundle([]Rule{
		{IPCIDR: []string{"0.0.0.0/0", "::/0"}, Outbound: OutboundDirect},
		{DomainSuffix: []string{"com"}, Outbound: OutboundDirect},
	}, nil)
	if got := b.Evaluate(domain("example.com")); got.RuleIndex != 1 {
		t.Fatalf("domain matched rule %d, want 1 (ip_cidr must not match a domain destination)", got.RuleIndex)
	}
	if got := b.Evaluate(ip("8.8.8.8")); got.RuleIndex != 0 {
		t.Fatalf("ip matched rule %d, want 0", got.RuleIndex)
	}
	if got := b.Evaluate(ip("2001:db8::1")); got.RuleIndex != 0 {
		t.Fatalf("v6 matched rule %d, want 0", got.RuleIndex)
	}
}

// rule_set 展开成文件里的规则;未知 tag 不命中(而不是 panic)。
func TestEvaluateExpandsRuleSets(t *testing.T) {
	b := bundle([]Rule{
		{RuleSet: []string{RuleSetChinaDomain}, Outbound: OutboundDirect},
		{RuleSet: []string{RuleSetChinaCIDR, "missing"}, Outbound: OutboundDirect},
	}, map[string]RuleSetFile{
		RuleSetChinaDomain: {Version: 1, Rules: []Rule{{DomainSuffix: []string{"qq.com"}}}},
		RuleSetChinaCIDR:   {Version: 1, Rules: []Rule{{IPCIDR: []string{"192.0.2.0/24"}}}},
	})
	if got := b.Evaluate(domain("im.qq.com")); got.RuleIndex != 0 {
		t.Fatalf("qq.com → rule %d, want 0", got.RuleIndex)
	}
	if got := b.Evaluate(ip("192.0.2.9")); got.RuleIndex != 1 {
		t.Fatalf("192.0.2.9 → rule %d, want 1", got.RuleIndex)
	}
	if got := b.Evaluate(ip("9.9.9.9")); got.RuleIndex != -1 {
		t.Fatalf("9.9.9.9 → rule %d, want final", got.RuleIndex)
	}
}

// 空目的地(既无域名也无 IP)落 final —— 与 bx「信息不足保守走代理」同向。
func TestEvaluateEmptyDestinationFallsToFinal(t *testing.T) {
	b := bundle([]Rule{{DomainSuffix: []string{"a.com"}, Outbound: OutboundDirect}}, nil)
	if got := b.Evaluate(Destination{}); got.Outbound != OutboundProxy || got.RuleIndex != -1 {
		t.Fatalf("got %+v", got)
	}
}
