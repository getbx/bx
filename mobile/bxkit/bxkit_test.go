package bxkit_test

import (
	"encoding/json"
	"net/netip"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/embedded"
	"github.com/getbx/bx/internal/explainwords"
	"github.com/getbx/bx/internal/route"
	"github.com/getbx/bx/internal/singboxrules"
	"github.com/getbx/bx/internal/supervisor"
	"github.com/getbx/bx/mobile/bxkit"
)

const policyJSON = `{"global":false,"direct":["zoom.us","*.A.com","192.0.2.4","2001:db8::/32"],"proxy":["*.zoom.us","X.A.com","198.51.100.0/24"]}`

type answer struct {
	Target  string `json:"target"`
	Kind    string `json:"kind"`
	Verdict string `json:"verdict"`
	Because string `json:"because"`
	Rule    string `json:"rule"`
}

func explain(t *testing.T, policy, target string) answer {
	t.Helper()
	raw, err := bxkit.Explain(policy, string(embedded.ChinaDomain()), string(embedded.ChinaCIDR()), target)
	if err != nil {
		t.Fatalf("Explain(%q): %v", target, err)
	}
	var a answer
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, raw)
	}
	return a
}

// 三角闭合:手机上的 explain、Mac 上的 route.Explain(生产路径 supervisor.BuildRouter)、手机数据面
// 真正执行的规则(singboxrules)对同一目的地给同一答案;措辞与 `bx explain` 同一张表。
func TestPhoneExplainAgreesWithTheMacAndWithThePhoneDataPlane(t *testing.T) {
	for _, global := range []bool{false, true} {
		policy := policyJSON
		if global {
			policy = strings.Replace(policy, `"global":false`, `"global":true`, 1)
		}
		yaml := "server: brook://example.invalid\nrules:\n  - direct: ['zoom.us', '*.A.com', '192.0.2.4', '2001:db8::/32']\n    proxy: ['*.zoom.us', 'X.A.com', '198.51.100.0/24']\n"
		if global {
			yaml += "global: true\n"
		}
		cfg, err := config.Parse([]byte(yaml))
		if err != nil {
			t.Fatal(err)
		}
		lists := []string{string(embedded.ChinaDomain()), string(embedded.ChinaCIDR())}
		mac, err := supervisor.BuildRouter(cfg, strings.Split(lists[0], "\n"), strings.Split(lists[1], "\n"))
		if err != nil {
			t.Fatal(err)
		}
		mac.GlobalProxy = cfg.Global
		phone, err := singboxrules.Translate(cfg, singboxrules.Lists{ChinaDomain: strings.Split(lists[0], "\n"), ChinaCIDR: strings.Split(lists[1], "\n")})
		if err != nil {
			t.Fatal(err)
		}
		for _, target := range []string{"zoom.us", "us04web.zoom.us", "x.a.com", "b.a.com", "qq.com", "www.baidu.com", "example.org", "Example.ORG.", "192.0.2.4", "198.51.100.7", "10.1.2.3", "203.0.113.5"} {
			got := explain(t, policy, target)
			var dec route.Decision
			var why route.Reason
			var dst singboxrules.Destination
			if ip, err := netip.ParseAddr(target); err == nil {
				dec, why = mac.ExplainIP(ip)
				dst = singboxrules.Destination{IP: ip}
			} else {
				dec, why = mac.Explain(route.Meta{Domain: target})
				dst = singboxrules.Destination{Domain: target}
			}
			want := map[route.Decision]string{route.Direct: "direct", route.Proxy: "tunnel"}[dec]
			if got.Verdict != want {
				t.Errorf("global=%v %s: phone explain says %s, Mac says %s (%s)", global, target, got.Verdict, want, why.Source)
			}
			if got.Because != explainwords.SourceLabel(why.Source.String()) || got.Rule != why.Rule {
				t.Errorf("global=%v %s: phone words %q/%q, Mac words %q/%q", global, target, got.Because, got.Rule, explainwords.SourceLabel(why.Source.String()), why.Rule)
			}
			plane := phone.Evaluate(dst).Outbound
			if map[string]string{"tunnel": singboxrules.OutboundProxy, "direct": singboxrules.OutboundDirect}[got.Verdict] != plane {
				t.Errorf("global=%v %s: phone explain says %s but the phone's rules route it to %s", global, target, got.Verdict, plane)
			}
		}
	}
}

// 手机上 v6 一律拒绝(mobileconfig 的前导规则),explain 必须如实说,不许照 route.Explain 说「走隧道」。
func TestPhoneExplainSaysIPv6IsBlocked(t *testing.T) {
	got := explain(t, policyJSON, "2606:4700::1")
	if got.Verdict != "blocked" || !strings.Contains(got.Because, "IPv6") {
		t.Fatalf("v6 answer = %+v", got)
	}
}

func TestPhoneExplainAcceptsAURLAndRefusesGarbage(t *testing.T) {
	if got := explain(t, policyJSON, "  https://Sub.Zoom.us/path?q=1 "); got.Target != "sub.zoom.us" || got.Verdict != "tunnel" {
		t.Fatalf("URL answer = %+v", got)
	}
	if _, err := bxkit.Explain(policyJSON, "", "", "   "); err == nil {
		t.Fatal("empty target must be an error")
	}
	if _, err := bxkit.Explain(`{"direct":`, "", "", "a.com"); err == nil {
		t.Fatal("malformed policy must be an error, not a silent default")
	}
}
