package singboxrules

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/route"
)

func parseConfig(t *testing.T, yaml string) *config.Config {
	t.Helper()
	cfg, err := config.Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("config.Parse: %v", err)
	}
	return cfg
}

const fixtureYAML = `
server: brook://example.invalid
rules:
  - proxy: ['*.zoom.us', 'X.A.com', '10.9.0.0/16']
    direct: ['zoom.us', '*.A.com', '1.2.3.4', '2001:db8::/32']
`

var fixtureLists = Lists{
	ChinaDomain: []string{"# comment", "", "*.baidu.com", "qq.com"},
	ChinaCIDR:   []string{"1.0.1.0/24", "# c", "", "2400:da00::/32"},
}

func ruleWith(t *testing.T, rules []Rule, pred func(Rule) bool, what string) (int, Rule) {
	t.Helper()
	for i, r := range rules {
		if pred(r) {
			return i, r
		}
	}
	t.Fatalf("no rule %s in %+v", what, rules)
	return -1, Rule{}
}

// 域名规则:proxy 在 direct 前(bx 先查 proxy),`*.` 去掉、小写。
func TestTranslateOrdersUserProxyBeforeUserDirectAndNormalisesSuffixes(t *testing.T) {
	b, err := Translate(parseConfig(t, fixtureYAML), fixtureLists)
	if err != nil {
		t.Fatal(err)
	}
	pi, proxy := ruleWith(t, b.Route.Rules, func(r Rule) bool { return r.Outbound == OutboundProxy && len(r.DomainSuffix) > 0 }, "proxy domain")
	di, direct := ruleWith(t, b.Route.Rules, func(r Rule) bool { return r.Outbound == OutboundDirect && len(r.DomainSuffix) > 0 }, "direct domain")
	if pi > di {
		t.Fatalf("proxy domain rule (#%d) comes after direct (#%d); bx checks proxy first", pi, di)
	}
	if want := []string{"zoom.us", "x.a.com"}; !slices.Equal(proxy.DomainSuffix, want) {
		t.Fatalf("proxy domain_suffix = %v, want %v", proxy.DomainSuffix, want)
	}
	if want := []string{"zoom.us", "a.com"}; !slices.Equal(direct.DomainSuffix, want) {
		t.Fatalf("direct domain_suffix = %v, want %v", direct.DomainSuffix, want)
	}
}

// 网段规则:裸 IP 补 /32,v6 原样;proxy 网段在 direct 网段前。
func TestTranslateSplitsCIDRRulesLikeBuildRouter(t *testing.T) {
	b, err := Translate(parseConfig(t, fixtureYAML), fixtureLists)
	if err != nil {
		t.Fatal(err)
	}
	pi, proxy := ruleWith(t, b.Route.Rules, func(r Rule) bool { return r.Outbound == OutboundProxy && len(r.IPCIDR) > 0 }, "proxy cidr")
	di, direct := ruleWith(t, b.Route.Rules, func(r Rule) bool {
		return r.Outbound == OutboundDirect && len(r.IPCIDR) > 0 && !slices.Contains(r.IPCIDR, "10.0.0.0/8")
	}, "direct cidr")
	if pi > di {
		t.Fatalf("proxy cidr rule (#%d) after direct (#%d)", pi, di)
	}
	if want := []string{"10.9.0.0/16"}; !slices.Equal(proxy.IPCIDR, want) {
		t.Fatalf("proxy ip_cidr = %v, want %v", proxy.IPCIDR, want)
	}
	if want := []string{"1.2.3.4/32", "2001:db8::/32"}; !slices.Equal(direct.IPCIDR, want) {
		t.Fatalf("direct ip_cidr = %v, want %v", direct.IPCIDR, want)
	}
}

// 私网恒直连,排在用户网段规则之后、china 之前;清单就是 route.DefaultPrivateCIDRs。
func TestTranslateKeepsPrivateNetworksDirectAfterUserCIDRsAndBeforeChina(t *testing.T) {
	b, err := Translate(parseConfig(t, fixtureYAML), fixtureLists)
	if err != nil {
		t.Fatal(err)
	}
	pi, private := ruleWith(t, b.Route.Rules, func(r Rule) bool { return slices.Contains(r.IPCIDR, "10.0.0.0/8") }, "private")
	if private.Outbound != OutboundDirect || !slices.Equal(private.IPCIDR, route.DefaultPrivateCIDRs) {
		t.Fatalf("private rule = %+v, want direct over %v", private, route.DefaultPrivateCIDRs)
	}
	ui, _ := ruleWith(t, b.Route.Rules, func(r Rule) bool { return slices.Contains(r.IPCIDR, "1.2.3.4/32") }, "user direct cidr")
	ci, _ := ruleWith(t, b.Route.Rules, func(r Rule) bool { return slices.Contains(r.RuleSet, RuleSetChinaCIDR) }, "china cidr")
	if !(ui < pi && pi < ci) {
		t.Fatalf("order user(%d) < private(%d) < china(%d) violated", ui, pi, ci)
	}
}

// china 两个 rule-set 各被引用一次,域名的排在用户域名规则之后;列表归一化与 NewDomainSet 同条。
func TestTranslateReferencesChinaRuleSetsAndNormalisesTheLists(t *testing.T) {
	b, err := Translate(parseConfig(t, fixtureYAML), fixtureLists)
	if err != nil {
		t.Fatal(err)
	}
	di, _ := ruleWith(t, b.Route.Rules, func(r Rule) bool { return r.Outbound == OutboundDirect && len(r.DomainSuffix) > 0 }, "direct domain")
	cdi, cd := ruleWith(t, b.Route.Rules, func(r Rule) bool { return slices.Contains(r.RuleSet, RuleSetChinaDomain) }, "china domain")
	if cd.Outbound != OutboundDirect || cdi < di {
		t.Fatalf("china domain rule = %+v at #%d (direct domain at #%d)", cd, cdi, di)
	}
	refs := 0
	for _, r := range b.Route.Rules {
		refs += len(r.RuleSet)
	}
	if refs != 2 {
		t.Fatalf("rule_set references = %d, want exactly 2", refs)
	}
	tags := []string{}
	for _, ref := range b.Route.RuleSet {
		tags = append(tags, ref.Tag)
		if ref.Type != "local" || ref.Format != "source" || ref.Path != ref.Tag+".json" {
			t.Fatalf("rule_set ref %+v: want local/source/<tag>.json", ref)
		}
	}
	slices.Sort(tags)
	if want := []string{RuleSetChinaCIDR, RuleSetChinaDomain}; !slices.Equal(tags, want) {
		t.Fatalf("rule_set tags = %v, want %v", tags, want)
	}
	dom := b.RuleSets[RuleSetChinaDomain]
	if dom.Version != 1 || len(dom.Rules) != 1 || !slices.Equal(dom.Rules[0].DomainSuffix, []string{"baidu.com", "qq.com"}) {
		t.Fatalf("china domain rule-set = %+v; comments/blank lines must go, *. must be stripped", dom)
	}
	cidr := b.RuleSets[RuleSetChinaCIDR]
	if cidr.Version != 1 || len(cidr.Rules) != 1 || !slices.Equal(cidr.Rules[0].IPCIDR, []string{"1.0.1.0/24", "2400:da00::/32"}) {
		t.Fatalf("china cidr rule-set = %+v", cidr)
	}
	if b.Route.Final != OutboundProxy {
		t.Fatalf("final = %q, want proxy (bx defaults to the tunnel)", b.Route.Final)
	}
}

// global: china 两个 rule-set 一条 rule 都不许引用;私网与用户规则照旧。
func TestTranslateGlobalDropsEveryChinaRule(t *testing.T) {
	b, err := Translate(parseConfig(t, fixtureYAML+"global: true\n"), fixtureLists)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range b.Route.Rules {
		if len(r.RuleSet) > 0 {
			t.Fatalf("global config still routes by china list: %+v", r)
		}
	}
	ruleWith(t, b.Route.Rules, func(r Rule) bool { return slices.Contains(r.IPCIDR, "10.0.0.0/8") }, "private (must survive global)")
	ruleWith(t, b.Route.Rules, func(r Rule) bool { return slices.Contains(r.DomainSuffix, "a.com") }, "user direct (must survive global)")
}

// via / egress 在手机上没有对应物:拒绝,不静默丢。
func TestTranslateRefusesNamedEgress(t *testing.T) {
	cfg := parseConfig(t, `
server: brook://example.invalid
egress:
  - name: office
    socks5: 127.0.0.1:1080
rules:
  - via: office
    cidr: ['10.84.0.0/16']
`)
	_, err := Translate(cfg, fixtureLists)
	if !errors.Is(err, ErrUnsupportedEgress) {
		t.Fatalf("err = %v, want ErrUnsupportedEgress: dropping a via rule silently routes those addresses differently on the phone", err)
	}
}

// 没有用户规则时不产出空 rule(sing-box 对空 rule 报错),但私网、china、final 照在。
func TestTranslateEmitsNoEmptyRules(t *testing.T) {
	b, err := Translate(parseConfig(t, "server: brook://example.invalid\n"), fixtureLists)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range b.Route.Rules {
		if len(r.DomainSuffix)+len(r.IPCIDR)+len(r.RuleSet) == 0 {
			t.Fatalf("empty rule emitted: %+v", r)
		}
	}
	if len(b.Route.Rules) != 3 { // private, china domain, china cidr
		t.Fatalf("rules = %d (%+v), want private + two china", len(b.Route.Rules), b.Route.Rules)
	}
}

// JSON 形状:键名是 sing-box 的,rule_set 声明与 final 在 route 下。
func TestBundleJSONUsesSingboxKeys(t *testing.T) {
	b, err := Translate(parseConfig(t, fixtureYAML), fixtureLists)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := b.RouteJSON()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"route"`, `"rules"`, `"domain_suffix"`, `"ip_cidr"`, `"rule_set"`, `"outbound"`, `"final":"proxy"`, `"tag":"bx-china-domain"`, `"type":"local"`, `"format":"source"`, `"path":"bx-china-domain.json"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("route JSON lacks %s:\n%s", want, raw)
		}
	}
	rs, err := b.RuleSetJSON(RuleSetChinaCIDR)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rs), `"version":1`) || !strings.Contains(string(rs), `"ip_cidr"`) || strings.Contains(string(rs), `"outbound"`) {
		t.Errorf("rule-set JSON must be {version, rules[{ip_cidr}]} without outbound:\n%s", rs)
	}
	if _, err := b.RuleSetJSON("nope"); err == nil {
		t.Error("unknown rule-set tag must error, not return an empty file")
	}
}
