package mobileconfig

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/singboxrules"
)

var testProxy = map[string]any{"type": "vless", "tag": "reality-out", "server": "203.0.113.9", "server_port": 443}

var testLists = singboxrules.Lists{ChinaDomain: []string{"qq.com"}, ChinaCIDR: []string{"192.0.2.0/24"}}

func build(t *testing.T, yaml string) (Files, map[string]any) {
	t.Helper()
	cfg, err := config.Parse([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	f, err := Build(cfg, testLists, testProxy)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(f.Config, &doc); err != nil {
		t.Fatalf("config is not JSON: %v", err)
	}
	return f, doc
}

const baseYAML = "server: brook://example.invalid\nrules:\n  - direct: ['a.com']\n"

func at(doc map[string]any, path ...string) any {
	var cur any = doc
	for _, p := range path {
		cur = cur.(map[string]any)[p]
	}
	return cur
}

// DNS 照桌面(internal/dns/server.go):tun 来的 A 查询答 fake-IP,其余类型空答(NODATA);
// 两条都只管 tun 来的查询,直连出站自己解析真实地址不受影响。fake-IP 段与桌面同一个常量。
func TestDNSMirrorsTheDesktopFakeIPHandler(t *testing.T) {
	_, doc := build(t, baseYAML)
	servers := at(doc, "dns", "servers").([]any)
	fake := servers[0].(map[string]any)
	if fake["type"] != "fakeip" || fake["inet4_range"] != config.DefaultFakeipCIDR {
		t.Fatalf("first dns server = %v, want fakeip over %s", fake, config.DefaultFakeipCIDR)
	}
	rules := at(doc, "dns", "rules").([]any)
	if len(rules) != 2 {
		t.Fatalf("dns rules = %v", rules)
	}
	a, rest := rules[0].(map[string]any), rules[1].(map[string]any)
	if !reflect.DeepEqual(a["inbound"], []any{tunTag}) || !reflect.DeepEqual(a["query_type"], []any{"A"}) || a["server"] != "fakeip" {
		t.Fatalf("A rule = %v", a)
	}
	if !reflect.DeepEqual(rest["inbound"], []any{tunTag}) || rest["invert"] != true || rest["action"] != "predefined" || rest["rcode"] != "NOERROR" {
		t.Fatalf("non-A rule = %v, want empty answer for every other type from the tun", rest)
	}
	if at(doc, "dns", "final") != "local" {
		t.Fatalf("dns final = %v (fakeip cannot be final in sing-box 1.14)", at(doc, "dns", "final"))
	}
}

// 路由:先 sniff、再 hijack-dns、再拒 v6(桌面 v6 是 fail-closed 阻断),然后**原样**是
// singboxrules 翻出来的那一串,final 走代理。
func TestRouteIsTheTranslatedRulesBehindThreeFixedPreambleRules(t *testing.T) {
	cfg, _ := config.Parse([]byte(baseYAML))
	want, err := singboxrules.Translate(cfg, testLists)
	if err != nil {
		t.Fatal(err)
	}
	_, doc := build(t, baseYAML)
	rules := at(doc, "route", "rules").([]any)
	pre := []map[string]any{{"action": "sniff"}, {"protocol": "dns", "action": "hijack-dns"}, {"ip_version": float64(6), "action": "reject"}}
	for i, p := range pre {
		if !reflect.DeepEqual(rules[i].(map[string]any), p) {
			t.Fatalf("route rule %d = %v, want %v", i, rules[i], p)
		}
	}
	var translated []any
	b, _ := json.Marshal(want.Route.Rules)
	_ = json.Unmarshal(b, &translated)
	if !reflect.DeepEqual(rules[len(pre):], translated) {
		t.Fatalf("route rules after the preamble differ from singboxrules.Translate:\ngot  %v\nwant %v", rules[len(pre):], translated)
	}
	if at(doc, "route", "final") != singboxrules.OutboundProxy {
		t.Fatalf("final = %v", at(doc, "route", "final"))
	}
	res := at(doc, "route", "default_domain_resolver").(map[string]any)
	if res["server"] != "local" || res["strategy"] != "ipv4_only" {
		t.Fatalf("default_domain_resolver = %v", res)
	}
}

// 出站三个,代理那个 tag 被改成 proxy(调用方给的 tag 不算数),且不改调用方的 map。
func TestOutboundsAreProxyDirectBlock(t *testing.T) {
	_, doc := build(t, baseYAML)
	obs := at(doc, "outbounds").([]any)
	var tags []any
	for _, o := range obs {
		tags = append(tags, o.(map[string]any)["tag"])
	}
	if !reflect.DeepEqual(tags, []any{"proxy", "direct", "block"}) {
		t.Fatalf("outbound tags = %v", tags)
	}
	if obs[0].(map[string]any)["server"] != "203.0.113.9" {
		t.Fatalf("proxy outbound lost its body: %v", obs[0])
	}
	if testProxy["tag"] != "reality-out" {
		t.Fatal("Build mutated the caller's outbound map")
	}
}

// tun 同时占住 v4 与 v6:只给 v4 时 iOS 会把 v6 从物理网卡放出去。
func TestTunClaimsBothAddressFamilies(t *testing.T) {
	_, doc := build(t, baseYAML)
	in := at(doc, "inbounds").([]any)[0].(map[string]any)
	if in["type"] != "tun" || in["tag"] != tunTag || in["auto_route"] != true || in["strict_route"] != true {
		t.Fatalf("tun inbound = %v", in)
	}
	addrs := in["address"].([]any)
	if len(addrs) != 2 {
		t.Fatalf("tun addresses = %v, want one v4 and one v6", addrs)
	}
}

func TestRuleSetFilesComeFromTheTranslation(t *testing.T) {
	f, _ := build(t, baseYAML)
	for _, tag := range []string{singboxrules.RuleSetChinaDomain, singboxrules.RuleSetChinaCIDR} {
		if len(f.RuleSets[tag+".json"]) == 0 {
			t.Fatalf("missing rule-set file %s.json", tag)
		}
	}
}

func TestBuildRefusesAMissingOutbound(t *testing.T) {
	cfg, _ := config.Parse([]byte(baseYAML))
	if _, err := Build(cfg, testLists, nil); err == nil {
		t.Fatal("nil outbound must be an error, not a config with no way out")
	}
}
