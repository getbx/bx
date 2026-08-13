package supervisor

import (
	"context"
	"net"
	"os"
	"strings"
	"testing"

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

// **顺序即优先级,而这条只能在接线层钉。**
//
// matchSplit 取第一个命中的路由;用户在 config 里写的必须排在 overlay 那组**前面**。
// 这件事不在任何纯函数里,只在 run.go 那几行里 —— 而本仓库这一轮反复栽在同一处:
// 判据对、接线错。读源码是这里唯一够得着的办法。
func TestUserSplitRulesArePrependedBeforeOverlayOnes(t *testing.T) {
	raw, err := os.ReadFile("run.go")
	if err != nil {
		t.Fatalf("读不到 run.go:%v —— 守卫失去意义,必须响亮失败", err)
	}
	source := string(raw)
	userAt := strings.Index(source, "for _, r := range cfg.DNS.Split {")
	overlayAt := strings.Index(source, "for _, r := range overlaySplit {")
	if userAt < 0 || overlayAt < 0 {
		t.Fatalf("找不到那两个循环(user@%d overlay@%d)—— 守卫读不懂现在的代码了", userAt, overlayAt)
	}
	if userAt > overlayAt {
		t.Error("overlay 的 split 排在了用户配置前面 —— matchSplit 取第一个命中的," +
			"于是用户为同一后缀配的解析器被硬编码的那个静默遮蔽")
	}
}

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
