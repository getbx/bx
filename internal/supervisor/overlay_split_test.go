package supervisor

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/overlay"
)

// **解析器地址必须带端口,否则每一次查询都 SERVFAIL。**
//
// config.Parse 会给 dns.split[].server 补 :53,而 overlay 那条路绕过了那次归一化。
// 不补的后果不是「少个端口」:DialContext 直接报 `missing port in address`,
// Respond 把它变成 SERVFAIL —— 于是在任何检测到 Tailscale 的机器上,**每一次
// ts.net 查询都失败**,正好是这个功能要修的那件事。
func TestNormalizeDNSServerAddr(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"100.100.100.100", "100.100.100.100:53"},
		{"100.100.100.100:53", "100.100.100.100:53"},
		{"100.100.100.100:5353", "100.100.100.100:5353"},
		{"fd7a:115c:a1e0::53", "[fd7a:115c:a1e0::53]:53"},
		{"[fd7a:115c:a1e0::53]", "[fd7a:115c:a1e0::53]:53"},
		{"[fd7a:115c:a1e0::53]:53", "[fd7a:115c:a1e0::53]:53"},
		{"", ""},
	} {
		if got := normalizeDNSServerAddr(tc.in); got != tc.want {
			t.Errorf("normalizeDNSServerAddr(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	// **判据不是「长得对」,是「拨得动」。** 上面那张表只能证明字符串形状,
	// 而真正会挂的是 Dial —— 所以直接拿它去问一次 net 包。
	for _, raw := range []string{"100.100.100.100", "fd7a:115c:a1e0::53"} {
		if _, _, err := net.SplitHostPort(normalizeDNSServerAddr(raw)); err != nil {
			t.Errorf("归一化之后仍然拆不出 host/port(%q):%v —— DialContext 会当场失败", raw, err)
		}
	}
}

// **用户的规则必须能覆盖内置的。**
//
// dns.Server.matchSplit 取**第一个**命中的路由,所以顺序就是优先级。早先这里把
// overlay 那组放在前面并写着「用户可以覆盖」—— 那句话与代码正好相反:用户为
// ts.net 配的解析器会被硬编码的那个静默遮蔽,而那个硬编码的当时还根本答不了。
func TestOverlaySplitPatternsCoverSuffixAndSubdomains(t *testing.T) {
	got := overlaySplitPatterns("ts.net")
	var sawSuffix, sawWildcard bool
	for _, p := range got {
		switch p {
		case "ts.net":
			sawSuffix = true
		case "*.ts.net":
			sawWildcard = true
		}
	}
	if !sawSuffix || !sawWildcard {
		t.Fatalf("= %v,后缀本身与子域都要覆盖(magicdns 名字是 host.tailnet.ts.net)", got)
	}
}

// **TestUserSplitRulesArePrependedBeforeOverlayOnes 2026-08-31 退场,记档在此。**
//
// 它比较两个 for 循环在 run.go 里出现的位置,并且自己写着「这件事不在任何纯
// 函数里,读源码是这里唯一够得着的办法」—— **那句话当时是真的,同时也是一份
// 待办**。判据现已抽成 buildSplitRoutes(splitbrain.go),由下面那条
// TestSplitRoutesPutUserRulesFirst 从行为上钉住(变异实测:把 overlay 排到
// 用户前面,它当场转红)。
//
// 顺带一提它退场的方式:两个循环一搬走,它自己就以「找不到那两个循环 ——
// 守卫读不懂现在的代码了」响亮失败,而不是安静地继续通过。**一条读源码的
// 守卫至少该有这个自觉**,这一点它做对了。

// **没有 Tailscale 的机器上,一条 DERP /32 都不许装。**
//
// 写死的公网 IP 是一种泄漏面:地址被回收给别人之后那条旁路仍在,于是发往陌生人的
// 流量绕过隧道、带着真实源地址出去。这条规矩 internal/overlay 为 ZeroTier 的兜底表
// 立过,而 Tailscale 的 DERP 表此前没有执行它 —— 去掉 darwin 门之后影响面还扩到了
// Linux 与 Windows(审查抓到的)。
func TestNoTailscaleMeansNoDERPBypass(t *testing.T) {
	// 没有任何租户在跑:初值必须是空的。
	if got := initialOverlayBypass(context.Background(), &net.Dialer{}, nil); len(got) != 0 {
		t.Errorf("没有 overlay 在跑却装了 %d 条旁路:%v —— 那是常开的绕过隧道的路", len(got), got)
	}
	// 只有 ZeroTier:只出它自己的兜底表,不去抓 DERP map。
	zt := []overlay.Tenant{{Name: "zerotier", RelayFallbackCIDRs: []string{"104.194.8.134/32"}}}
	got := initialOverlayBypass(context.Background(), &net.Dialer{}, zt)
	if len(got) != 1 || got[0] != "104.194.8.134/32" {
		t.Errorf("= %v,只该有 ZeroTier 自己的那条", got)
	}
}

// 抓取那一侧同理:没有 Tailscale 就不抓,而且要走「失败」那条路 ——
// 保留上一份、退避重试,好让 Tailscale 装上之后下一轮自然看见它。
func TestBypassFetchSkipsDERPWhenTailscaleIsAbsent(t *testing.T) {
	if overlayPresent(nil, "tailscale") {
		t.Fatal("空集合里不该认出 tailscale")
	}
	if !overlayPresent([]overlay.Tenant{{Name: "tailscale"}}, "tailscale") {
		t.Fatal("在跑时必须认出来")
	}
	if errNoTailscaleForBypass == nil {
		t.Fatal("需要一个哨兵错误,让循环把「没抓」当成一次未成功而不是权威答案")
	}
}

// **「Tailscale 不在这台机器上」不是抓取失败,而 ZeroTier 的旁路不许被它连累。**
//
// overlay_signals.go 里那句注释写的就是「这次没抓,不是失败」,而代码把它当成
// 失败往上抛:overlayAwareBypassFetch 一见 error 就 `return nil, err`,租户兜底
// 那一半根本走不到。后果是**一台只跑 ZeroTier 的机器,中继旁路永远装不上** ——
// 而重试循环还会一直重试同一个必然失败的抓取,把退避推到长周期。
//
// 注释与代码分叉时,分叉本身就是缺陷;这条钉的是代码那一侧。
func TestAbsentTailscaleStillPublishesOtherTenantsBypass(t *testing.T) {
	fetch := overlayAwareBypassFetch(
		func(context.Context) ([]string, error) { return nil, errNoTailscaleForBypass },
		func() []string { return []string{"104.194.8.134/32"} }, // ZeroTier 的兜底根
	)
	got, err := fetch(context.Background())
	if err != nil {
		t.Fatalf("Tailscale 不在场被当成了失败:%v —— ZeroTier 的旁路会因此永远装不上", err)
	}
	if len(got) != 1 || got[0] != "104.194.8.134/32" {
		t.Fatalf("租户兜底没被发布:%v", got)
	}
}

// 反过来:**真的抓取失败仍然必须如实上报**,否则 decideBypassUpdate 会把一次失败
// 当成「成功地得到了一个更短的答案」,把已经装好的 DERP 旁路撤掉。
func TestRealFetchFailureIsStillReportedAsFailure(t *testing.T) {
	boom := errors.New("dial controlplane.tailscale.com: timeout")
	fetch := overlayAwareBypassFetch(
		func(context.Context) ([]string, error) { return nil, boom },
		func() []string { return []string{"104.194.8.134/32"} },
	)
	if _, err := fetch(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("真实抓取失败被吞了:%v —— 那会让已装好的旁路被一次超时撤掉", err)
	}
}

// **有冒号没端口的写法必须落回 53,而不是拼出一个拨不通的地址。**
//
// `10.0.13.23:` 会让 SplitHostPort 成功返回 port=""。上一版要求 port 非空才走
// 这一支,于是它掉进兜底分支,把整个 `10.0.13.23:` 当主机名再拼一次端口,
// 得到 `10.0.13.23::53` —— 而且是静默的:那个租户的整个命名空间从此全部超时。
func TestNormalizeDNSServerAddrHandlesColonWithoutPort(t *testing.T) {
	for in, want := range map[string]string{
		"10.0.13.23":      "10.0.13.23:53",
		"10.0.13.23:":     "10.0.13.23:53",
		"10.0.13.23:5353": "10.0.13.23:5353",
		"100.100.100.100": "100.100.100.100:53",
		"[fd7a::1]":       "[fd7a::1]:53",
		"[fd7a::1]:53":    "[fd7a::1]:53",
		"":                "",
	} {
		if got := normalizeDNSServerAddr(in); got != want {
			t.Errorf("normalizeDNSServerAddr(%q) = %q, want %q", in, got, want)
		}
	}
}

// **顺序即优先级,现在由一个纯函数带着,而不是靠读源码。**
//
// 上面那条守卫(TestUserSplitRulesArePrependedBeforeOverlayOnes,2026-08-31
// 退场)写着「这件事不在任何纯函数里,读源码是这里唯一够得着的办法」—— 那句话
// 在当时是真的,而它同时也是一份**待办**:把接线里的判据抽出来,守卫就不必再
// 猜源码。抽出来之后判据是行为:同一个后缀同时被用户与 overlay 配置时,
// 排在前面的必须是用户那条(matchSplit 取第一个命中的)。
func TestSplitRoutesPutUserRulesFirst(t *testing.T) {
	routes := buildSplitRoutes(
		[]config.SplitRule{{Domains: []string{"ts.net"}, Server: "10.0.0.1:53"}},
		[]overlay.SplitRoute{{Suffix: "ts.net", Resolver: "100.100.100.100"}},
	)
	if len(routes) != 2 {
		t.Fatalf("两边各一条,应当得到 2 条: %+v", routes)
	}
	// 第一条必须是用户那条 —— 否则用户为 ts.net 配的解析器被硬编码的那个静默遮蔽。
	if routes[0].Server != "10.0.0.1:53" {
		t.Fatalf("第一条不是用户的规则(server=%q)—— matchSplit 取第一个命中的,"+
			"用户配置会被 overlay 那条遮蔽", routes[0].Server)
	}
	if !routes[1].Match.Match("host.ts.net") {
		t.Fatalf("overlay 那条没覆盖子域: %+v", routes[1])
	}
}

// overlay 的解析器地址要补端口(它给的是裸 IP),而用户写的原样用 ——
// 用户可能故意指了非 53 端口。
func TestSplitRoutesNormalizeOnlyTheOverlayResolver(t *testing.T) {
	routes := buildSplitRoutes(
		[]config.SplitRule{{Domains: []string{"corp.example"}, Server: "10.0.0.1:5353"}},
		[]overlay.SplitRoute{{Suffix: "ts.net", Resolver: "100.100.100.100"}},
	)
	if routes[0].Server != "10.0.0.1:5353" {
		t.Fatalf("用户写的端口被改了: %q", routes[0].Server)
	}
	if routes[1].Server != "100.100.100.100:53" {
		t.Fatalf("overlay 的裸 IP 没补端口: %q", routes[1].Server)
	}
}

// 两边都空时不该产出任何路由 —— 调用方据此决定要不要启用 split-DNS,
// 一条空规则会让它启用一个什么都不匹配的转发器。
func TestSplitRoutesAreEmptyWhenNeitherSideHasRules(t *testing.T) {
	if routes := buildSplitRoutes(nil, nil); len(routes) != 0 {
		t.Fatalf("两边都空却产出了 %d 条", len(routes))
	}
}
