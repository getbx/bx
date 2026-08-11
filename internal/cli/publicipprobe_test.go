package cli

import (
	"net/url"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/embedded"
	"github.com/getbx/bx/internal/route"
)

// 出口探测用的域名一个都不许在 china 直连列表里。
//
// **在列表里的域名,保护开着时走的是直连**,于是探测报出来的是用户真实的 ISP
// 出口而不是隧道出口。后果不是「少一条信息」,是方向相反的谎:用户敲 bx doctor
// 想确认保护有没有生效,看到自己的真实 IP,得出的结论正好是「bx 在漏」。
//
// **这个坑踩过两次,而第二次是文档自己造成的。** CLAUDE.md 那条教训正确地点名了
// ifconfig.me 与 ip.sb「在 china 列表里,别用」,却在同一句话里推荐 api.ipify.org
// —— 而 ipify.org 同样在列表里(china_domain.txt:6045)。那条写错的推荐照着进了
// 代码三处,一直跑到 2026-08-11 才被发现。
//
// 所以这条守卫**不查一份人手维护的黑名单**,而是拿真实的内嵌列表 + 生产用的同一个
// DomainSet 逐个比对 —— 靠「记得选对」正是上一次失败的方式。
func TestPublicIPProbeDomainsAreNotChinaDirect(t *testing.T) {
	china := route.NewDomainSet(chinaDomainPatternsForTest(t))

	probes := append([]string{publicIPProbeV4URL, publicIPProbeV6URL}, publicIPProbeURLs...)
	if len(probes) < 3 {
		t.Fatalf("探测地址少得反常(%d 个)—— 这条守卫可能已经读不到它要守的东西了", len(probes))
	}

	for _, raw := range probes {
		parsed, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("探测地址 %q 解析不了: %v", raw, err)
		}
		host := parsed.Hostname()
		if host == "" {
			t.Fatalf("探测地址 %q 没有主机名", raw)
		}
		if china.Match(host) {
			t.Errorf("出口探测用了 %q,而它命中 china 直连列表 —— 保护开着时这条探测走直连,"+
				"报出来的是用户真实的 ISP 出口,于是 bx doctor 会把一台工作正常的机器说成在漏。"+
				"换一个不在列表里的域名(icanhazip.com / ipinfo.io 已核过不在)", host)
		}
	}
}

// 这条守卫的前提:那份列表真的被读进来了。读不到就必须响亮失败 ——
// 一个因为拿不到列表而自动通过的守卫,与没有守卫是同一回事。
func chinaDomainPatternsForTest(t *testing.T) []string {
	t.Helper()
	raw := embedded.ChinaDomain()
	if len(raw) == 0 {
		t.Fatal("内嵌的 china 域名列表是空的 —— 这条守卫失去意义,必须响亮失败而不是放过")
	}
	var patterns []string
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		patterns = append(patterns, line)
	}
	if len(patterns) < 1000 {
		t.Fatalf("只解析出 %d 条 china 域名,远少于预期 —— 列表格式变了,守卫已经读不懂它", len(patterns))
	}
	return patterns
}
