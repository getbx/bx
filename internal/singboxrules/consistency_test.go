package singboxrules_test

import (
	"fmt"
	"math/rand/v2"
	"net/netip"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/embedded"
	"github.com/getbx/bx/internal/route"
	"github.com/getbx/bx/internal/singboxrules"
	"github.com/getbx/bx/internal/supervisor"
)

// 这条守卫是整个包存在的理由(spec §3):同一份配置、同一个目的地,手机上的判定必须
// == Mac 上 route.Explain 的判定。桌面那半走**生产路径**(config.Parse → supervisor.BuildRouter
// → cfg.Global 照 splitbrain 的写法赋给 GlobalProxy → route.Explain),不在测试里重算一遍
// 「谁盖住谁」;手机那半是 Translate + Evaluate。两边分歧当场点名输入与两边依据。
//
// 要让缺陷回来,什么必须改变:翻译器的顺序、归一化、global 分支、私网清单,或 Evaluate
// 的语义 —— 任一处变了,下面某一组输入就会分歧。三条变异各咬中一条(见 plan Task 4)。

const consistencyYAML = `
server: brook://example.invalid
rules:
  - proxy: ['*.zoom.us', 'X.A.com', '10.9.0.0/16', 'CDN.qq.com']
    direct: ['zoom.us', '*.A.com', '1.2.3.4', '2001:db8::/32', 'proxy-me.example', '8.8.8.0/24']
  - proxy: ['proxy-me.example']
`

func embeddedLines(t *testing.T, raw []byte, what string) []string {
	t.Helper()
	if len(raw) == 0 {
		t.Fatalf("内嵌的 %s 是空的 —— 守卫会在空集合上恒真,必须响亮失败", what)
	}
	return strings.Split(string(raw), "\n")
}

func outboundOf(dec route.Decision) string {
	switch dec {
	case route.Direct:
		return singboxrules.OutboundDirect
	case route.Proxy:
		return singboxrules.OutboundProxy
	}
	return "<" + dec.String() + ">"
}

func TestTranslatedRulesAgreeWithRouteExplainOnEveryInput(t *testing.T) {
	chinaDomain := embeddedLines(t, embedded.ChinaDomain(), "china 域名列表")
	chinaCIDR := embeddedLines(t, embedded.ChinaCIDR(), "china 网段列表")
	rng := rand.New(rand.NewPCG(2026, 929)) // 固定种子:分歧要能复现

	// 输入:每条用户规则的本体 / 子域 / 前缀攻击 / 大写尾点;china 列表抽样 300 条同样四变;
	// IP:用户网段内外、私网、china 网段抽样、公网、v6。
	var domains []string
	for _, base := range []string{"zoom.us", "x.a.com", "a.com", "cdn.qq.com", "qq.com", "proxy-me.example", "example.org", "google.com"} {
		domains = append(domains, base, "sub."+base, "deep.sub."+base, "evil"+base, strings.ToUpper(base)+".")
	}
	sample := func(lines []string) []string {
		var picked []string
		for _, l := range lines {
			l = strings.TrimSpace(l)
			if l == "" || strings.HasPrefix(l, "#") {
				continue
			}
			picked = append(picked, l)
		}
		rng.Shuffle(len(picked), func(i, j int) { picked[i], picked[j] = picked[i], picked[j] })
		if len(picked) > 300 {
			picked = picked[:300]
		}
		return picked
	}
	for _, d := range sample(chinaDomain) {
		d = strings.TrimPrefix(d, "*.")
		domains = append(domains, d, "www."+d, "evil"+d, strings.ToUpper(d))
	}
	var ips []netip.Addr
	for _, s := range []string{"10.9.1.1", "10.8.1.1", "1.2.3.4", "1.2.3.5", "2001:db8::1", "2001:db9::1", "8.8.8.8", "8.8.4.4",
		"192.168.1.1", "172.16.0.1", "100.64.0.1", "169.254.1.1", "127.0.0.1", "1.1.1.1", "203.0.113.9", "2400:da00::1", "2606:4700::1"} {
		ips = append(ips, netip.MustParseAddr(s))
	}
	for _, c := range sample(chinaCIDR) {
		p, err := netip.ParsePrefix(c)
		if err != nil {
			continue
		}
		ips = append(ips, p.Addr(), p.Addr().Next())
	}

	for _, global := range []bool{false, true} {
		t.Run(fmt.Sprintf("global=%v", global), func(t *testing.T) {
			yaml := consistencyYAML
			if global {
				yaml += "global: true\n"
			}
			cfg, err := config.Parse([]byte(yaml))
			if err != nil {
				t.Fatal(err)
			}
			router, err := supervisor.BuildRouter(cfg, chinaDomain, chinaCIDR)
			if err != nil {
				t.Fatal(err)
			}
			router.GlobalProxy = cfg.Global // 与 supervisor/splitbrain.go 同一句
			bundle, err := singboxrules.Translate(cfg, singboxrules.Lists{ChinaDomain: chinaDomain, ChinaCIDR: chinaCIDR})
			if err != nil {
				t.Fatal(err)
			}
			checked := 0
			for _, d := range domains {
				dec, why := router.Explain(route.Meta{Domain: d})
				got := bundle.Evaluate(singboxrules.Destination{Domain: d})
				if outboundOf(dec) != got.Outbound {
					t.Errorf("domain %q: Mac says %s (%s %q), phone says %s (rule %d)", d, outboundOf(dec), why.Source, why.Rule, got.Outbound, got.RuleIndex)
				}
				checked++
			}
			for _, ip := range ips {
				dec, why := router.ExplainIP(ip)
				got := bundle.Evaluate(singboxrules.Destination{IP: ip})
				if outboundOf(dec) != got.Outbound {
					t.Errorf("ip %s: Mac says %s (%s), phone says %s (rule %d)", ip, outboundOf(dec), why.Source, got.Outbound, got.RuleIndex)
				}
				checked++
			}
			if checked < 1000 {
				t.Fatalf("only %d inputs compared — the sample collapsed, this guard proves little", checked)
			}
			// 自检:两边不是恒同一个答案(否则「永远 proxy」也能全绿)。
			seen := map[string]bool{}
			for _, d := range domains {
				seen[bundle.Evaluate(singboxrules.Destination{Domain: d}).Outbound] = true
			}
			if !seen[singboxrules.OutboundDirect] || !seen[singboxrules.OutboundProxy] {
				t.Fatalf("inputs never exercised both outbounds: %v", seen)
			}
		})
	}
}
