package leakcheck

import (
	"net/url"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/embedded"
	"github.com/getbx/bx/internal/route"
)

// 这三个地址是**用户可见契约**:页面在联网前照原样显示它们,换掉它们等于换掉
// 用户的暴露对象。所以改这三个串必须先改这条测试,不能顺手改。
func TestEndpointsArePinned(t *testing.T) {
	if EchoV4URL != "https://ipv4.icanhazip.com" {
		t.Errorf("v4 回声端变了:%q", EchoV4URL)
	}
	if EchoV6URL != "https://ipv6.icanhazip.com" {
		t.Errorf("v6 回声端变了:%q", EchoV6URL)
	}
	if STUNURL != "stun:stun.cloudflare.com:3478" {
		t.Errorf("STUN 变了:%q", STUNURL)
	}
	if TraceURL != "https://www.cloudflare.com/cdn-cgi/trace" {
		t.Errorf("trace 端点变了:%q", TraceURL)
	}
	d := Endpoints()
	if d.Trace != TraceURL {
		t.Errorf("Endpoints() 漏带 trace:%+v", d)
	}
	if d.EchoV4 != EchoV4URL || d.EchoV6 != EchoV6URL || d.STUN != STUNURL {
		t.Fatalf("Endpoints() 必须原样带出三个常量,得到 %+v", d)
	}
}

// v4 与 v6 必须是**两个不同的主机名**。用同一个主机名(比如某个双栈地址)就
// 问不出「v6 出口是谁」,而那正是别的 VPN 最常见的漏洞。
func TestEchoEndpointsAreTwoDistinctHosts(t *testing.T) {
	v4, err := url.Parse(EchoV4URL)
	if err != nil {
		t.Fatal(err)
	}
	v6, err := url.Parse(EchoV6URL)
	if err != nil {
		t.Fatal(err)
	}
	if v4.Hostname() == v6.Hostname() {
		t.Fatalf("v4 与 v6 回声端不能是同一个主机名(%q):双栈主机名问不出 v6 出口", v4.Hostname())
	}
	trace, err := url.Parse(TraceURL)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []*url.URL{v4, v6, trace} {
		if u.Scheme != "https" {
			t.Errorf("%s 必须是 https:明文回声在路上可被改写,判据就整个失效", u)
		}
	}
}

// **这条测试是为一个真实发生过的错误写的。** 项目拿 ifconfig.me 当出口探测,
// 而它本就在直连列表里,于是「漏直连」是自摆乌龙。同一个坑后来在
// internal/cli 的出口探测里**又犯了一次**(`api.ipify.org`,而 `ipify.org` 在
// 列表第 6045 行)—— 那次是文档自己推荐错了域名,推荐照着进了代码。
// **两处今天都已修好,而且都不再靠记忆**:那边是
// `TestPublicIPProbeDomainsAreNotChinaDirect`(同样拿真实内嵌列表 + 生产
// `DomainSet` 逐个比),现用 `icanhazip.com` / `ipinfo.io`;这边是本条测试。
//
// (此前这里写的是「同一个坑**今天还活在** internal/cli 里」—— 2026-08-24
// 复核发现那个说法已经过期。**一条声称某个 bug 仍然活着的注释,比一条普通
// 的陈旧注释更坏**:它会派下一个人去修一个不存在的东西,或者让他连带不再
// 相信旁边那些还成立的话。)
//
// 判据用的就是生产环境那一份 DomainSet 与那一份内嵌列表 —— 不是抄一份规则。
func TestEchoEndpointsAreNotOnTheChinaDirectList(t *testing.T) {
	patterns := strings.Split(string(embedded.ChinaDomain()), "\n")
	if len(patterns) < 1000 {
		t.Fatalf("内嵌 china 列表只有 %d 行,本守卫读不懂现在的资产,请连同它一起重写", len(patterns))
	}
	set := route.NewDomainSet(patterns)
	// 自检:列表里确实有东西能命中,否则下面的「没命中」是假绿。
	if !set.Match("ipify.org") {
		t.Fatal("自检失败:ipify.org 本应在 china 直连列表里(第 6045 行)——" +
			"若上游列表变了,请更新这条自检,而不是删掉它")
	}
	for _, raw := range []string{EchoV4URL, EchoV6URL, TraceURL} {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if set.Match(u.Hostname()) {
			t.Errorf("%s 在 china 直连列表里:bx 开着时它会直连,回声报出真实 ISP 出口,"+
				"把一台完全正常的机器判成泄漏(ifconfig.me 那个坑)", u.Hostname())
		}
	}
	// STUN 的主机名同理:它是 UDP,不走 DomainSet,但域名维度的分流规则一样适用。
	host := strings.TrimPrefix(STUNURL, "stun:")
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	if set.Match(host) {
		t.Errorf("STUN 主机 %s 在 china 直连列表里,srflx 会报出真实出口", host)
	}
}

// 端点是用户可见契约(页面联网前原样显示),逐个钉死。
func TestReachTargetsArePinned(t *testing.T) {
	want := []string{
		"https://api.anthropic.com/v1/messages",
		"https://claude.ai/favicon.ico",
		"https://api.openai.com/v1/models",
		"https://generativelanguage.googleapis.com/v1beta/models",
	}
	got := ReachTargets()
	if len(got) != len(want) {
		t.Fatalf("端点数 = %d, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].URL != w {
			t.Fatalf("第 %d 个 = %q, want %q", i, got[i].URL, w)
		}
	}
}

// 明文回声在路上可被改写,判据就整个失效。
func TestReachTargetsAreAllHTTPS(t *testing.T) {
	for _, tgt := range ReachTargets() {
		if !strings.HasPrefix(tgt.URL, "https://") {
			t.Fatalf("%s 不是 https —— %q", tgt.ID, tgt.URL)
		}
	}
}

// 每个端点连同「上次实测见到什么」一起记档。行为变了守卫会红一次,
// 那正是回来重测的时刻(spec §4.3)。
func TestEveryReachTargetRecordsWhatItLastReturned(t *testing.T) {
	for _, tgt := range ReachTargets() {
		if strings.TrimSpace(tgt.ExpectedSignal) == "" {
			t.Fatalf("%s 没有记录预期信号 —— 改端点的人无从判断它的行为变没变", tgt.ID)
		}
	}
}

// 第三关(spec §4.2):不是禁止端点落在内建 china 直连列表里,而是**如实报告**
// 它在不在 —— 可达性探测走哪条路由由本功能自己指定,不依赖分流,所以「在列表里」
// 不是缺陷,是这次探测经了哪条路的解释。
//
// **判据走生产那份 DomainSet,不在测试里重算一遍。**
func TestReachTargetsDeclareWhetherTheyAreOnTheChinaDirectList(t *testing.T) {
	for _, tgt := range ReachTargets() {
		if tgt.OnChinaDirectList == nil {
			t.Fatalf("%s 没有声明它在不在内建直连列表 —— 那是「这次探测经了哪条路」"+
				"唯一的解释来源(spec §4.2)", tgt.ID)
		}
	}
}

// chatgpt.com 实测恒为 CF 挑战页 ⇒ 恒 undetermined ⇒ 一行永远给不出答案的噪声。
// **这条守卫是给「顺手补全」的下一个人看的**:它不是漏了,是刻意不放。
func TestChatGPTIsDeliberatelyAbsentFromReachTargets(t *testing.T) {
	for _, tgt := range ReachTargets() {
		if strings.Contains(tgt.URL, "chatgpt.com") {
			t.Fatal("chatgpt.com 进了清单 —— 它恒为 CF 挑战页,会变成一行永远" +
				"「没查出来」的噪声;OpenAI 由 api.openai.com 代表(spec §4.1)")
		}
	}
}

// **协调者裁决**:ReachTarget.OnChinaDirectList 的值必须是写死的字面量,
// 不能在生产代码里运行时算(它不是「编译期常量」——Go 不允许对字面量取地址进
// const,是一个运行期 `*bool`,类型系统不替它背书)—— 本包纯度守卫的
// allowedInternalDeps 白名单刻意很窄(只有 internal/tristate 与
// internal/protectionstate),运行时判 china 列表要 import internal/route,
// 会撑开这个白名单。
//
// 但写死不等于可以凭空写:这条测试拿**真实内嵌列表 + 生产的 route.NewDomainSet**
// (与 TestEchoEndpointsAreNotOnTheChinaDirectList 同一手法)去核对 ReachTargets()
// 里每个 target 写死的值,核对的是「写死的值」与「实测出来的值」是否一致 ——
// 端点在列表里不是缺陷,写错了才是。
func TestReachTargetsOnChinaDirectListMatchesRealList(t *testing.T) {
	patterns := strings.Split(string(embedded.ChinaDomain()), "\n")
	if len(patterns) < 1000 {
		t.Fatalf("内嵌 china 列表只有 %d 行,本守卫读不懂现在的资产,请连同它一起重写", len(patterns))
	}
	set := route.NewDomainSet(patterns)
	// 自检:列表里确实有东西能命中,否则下面的比对可能只是列表没加载。
	if !set.Match("ipify.org") {
		t.Fatal("自检失败:ipify.org 本应在 china 直连列表里(第 6045 行)——" +
			"若上游列表变了,请更新这条自检,而不是删掉它")
	}
	for _, tgt := range ReachTargets() {
		u, err := url.Parse(tgt.URL)
		if err != nil {
			t.Fatal(err)
		}
		if tgt.OnChinaDirectList == nil {
			t.Fatalf("%s 没有声明 OnChinaDirectList", tgt.ID)
		}
		want := set.Match(u.Hostname())
		if *tgt.OnChinaDirectList != want {
			t.Errorf("%s(%s)写死的 OnChinaDirectList=%v,实测 DomainSet.Match=%v —— 不一致,"+
				"请去实测那个域名再改写死的值(不是改这条测试)",
				tgt.ID, u.Hostname(), *tgt.OnChinaDirectList, want)
		}
	}
}
