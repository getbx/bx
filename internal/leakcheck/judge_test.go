package leakcheck

import (
	"strings"
	"testing"

	"github.com/getbx/bx/internal/tristate"
)

// bxRunningNoV6Facts 是**生产环境真实送进来的**那一份本机事实:项目所有者的 Mac,
// bx 开着 —— v6 被 bx 的黑洞 reject 掉(IPv6DefaultPresent = False)、解析器是
// 127.0.0.1、v4 默认路由归 bx 自己的 utun11。
//
// **这份 fixture 是承重的。** 原来这两条测试喂的是 `LocalFacts{}`,而零值的
// IPv6DefaultPresent 是 Unknown —— IPv6 那条规则在读到 ok 分支之前就短路了,
// 于是它们看起来在守「空上报不许判 ok」,实际上守的是「本机事实也全空时不许判
// ok」。真机上那句 ok 照样发生,而两条测试全绿。
func bxRunningNoV6Facts() LocalFacts {
	return LocalFacts{
		DefaultRouteV4:     InterfaceRef{Name: "utun11", Display: "bx (utun11)"},
		DefaultRouteV6:     InterfaceRef{Err: "no route"},
		IPv6DefaultPresent: tristate.False,
		DNSServers:         []string{"127.0.0.1"},
		DNSServerEgress:    map[string]string{"127.0.0.1": "lo0"},
		BXTunInterface:     "utun11",
		BXProtection:       "protected",
	}
}

// **设计风险四。** 浏览器那一半从来没到过 —— 用户点了菜单、页面开了却没点
// 「Run the check」、或者直接关掉标签页,硬超时后 Wait 合成的正是这个零值上报,
// 而本机事实那一半是**真的**(CLI 的闭包里捕获的就是它)。
//
// **依赖浏览器的两条结论必须全是 not checked。** 尤其 IPv6 那条不许说
// 「no IPv6 exit was observed」—— 那是在断言一次从没发生过的观测。
func TestEmptyBrowserReportYieldsNotChecked(t *testing.T) {
	for _, tc := range []struct {
		name  string
		local LocalFacts
	}{
		// 真机形状:本机那一半齐全,只有浏览器那一半没到。
		{"bx 在跑、没有 v6、解析器在本机", bxRunningNoV6Facts()},
		// 两半都没有(非 darwin、或本机采集也全失败)。
		{"两半都没有", LocalFacts{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rep := Judge(fixedTime(), BrowserReport{}, tc.local)

			if len(rep.Findings) != 4 {
				t.Fatalf("结论条数应恒为 4(webrtc / ipv6 / dns / local_addresses),得到 %d",
					len(rep.Findings))
			}
			// **规则写成一句话,而不是按 fixture 分支**:一条结论只有在**本机那一半
			// 也答不了它**的时候才因为浏览器缺席而变成 not checked。
			//
			// DNS 只吃本机事实;IPv6 在「确知没有通往 v6 互联网的路」时同样只吃
			// 本机事实 —— 没有路就漏不出去,回声说什么都不改变它。把它们一并压成
			// not checked 是朝反方向撒谎(明明查过)。
			settledLocally := map[string]bool{
				FindingDNS:  true,
				FindingIPv6: tc.local.IPv6DefaultPresent == tristate.False,
			}
			for _, f := range rep.Findings {
				if settledLocally[f.ID] {
					continue
				}
				if f.Verdict != NotChecked {
					t.Errorf("%s 依赖浏览器那一半,而那一半从来没到过,必须是 not checked,"+
						"得到 %s(summary=%q)", f.ID, f.Verdict, f.Summary)
				}
				if f.Summary == "" {
					t.Errorf("%s 是 not checked 也必须说清为什么没问出来,summary 不能是空串", f.ID)
				}
			}
			if rep.AnomalyCount != 0 {
				t.Fatalf("全 not checked 时异常数必须是 0,得到 %d", rep.AnomalyCount)
			}
		})
	}
}

// **不许断言一次从没发生过的观测。**
//
// 上面那条只查三态;这条查措辞。「no IPv6 exit was observed」在页面从没联网时
// 是一句假话,而它恰恰是用户唯一会读的那一行。
func TestSilentBrowserNeverClaimsAnObservation(t *testing.T) {
	rep := Judge(fixedTime(), BrowserReport{}, bxRunningNoV6Facts())
	for _, f := range rep.Findings {
		if f.ID == FindingDNS {
			continue
		}
		text := f.Summary + " " + strings.Join(f.Evidence, " ")
		// **这一半对每一条结论都成立,与它判成什么无关。** 浏览器没联网,
		// 就没有任何一条结论可以引用一次浏览器观测 —— 这是 Critical 真正守的东西。
		for _, forbidden := range []string{"was observed", "no IPv6 exit"} {
			if strings.Contains(text, forbidden) {
				t.Errorf("%s 在浏览器那一半从没到过时出现了 %q —— 那是在断言一次"+
					"从没发生过的观测:%q", f.ID, forbidden, text)
			}
		}
		// **而这一半只对「确实需要浏览器」的结论成立。**
		//
		// 2026-08-11 用户指出:本机确知没有通往 IPv6 互联网的路,这件事本身就
		// 排除了 IPv6 泄漏 —— 回声说什么都不改变它。那种 ok 是**纯本机观测**
		// 支撑的,措辞里没有、也不需要「浏览器从没到过」这句话。
		// 原来这条守卫把两件事捆在一起,于是一条完全诚实的本地结论也被要求
		// 去解释一个它根本不依赖的东西。
		if f.Verdict != NotChecked {
			continue
		}
		if !strings.Contains(strings.ToLower(text), "never") {
			t.Errorf("%s 判成 not checked 就必须说清「浏览器那一半从来没到过」,得到 %q", f.ID, text)
		}
	}
}

// **「跑过、三项全失败」与「从来没跑」必须分开。**
//
// 前者是诚实的 ok:确知本机没有 v6 默认路由 + 浏览器确实试过 v6 回声并失败
// ⇒ 没有可漏的 v6 通路。把这一支一并压成 not checked,IPv6 那条就退化成
// 「在没有 v6 的机器上恒为没查」—— 恒绿的镜像,一样把这一行变成装饰。
func TestBrowserThatRanAndFailedStillSupportsTheHonestOK(t *testing.T) {
	browser := BrowserReport{
		ExitV4Err: "load failed",
		ExitV6Err: "load failed",
		STUNErr:   "ICE gathering failed",
	}
	f := ipv6Finding(t, browser, bxRunningNoV6Facts())
	if f.Verdict != OK {
		t.Fatalf("浏览器跑过且确知本机没有 v6 默认路由时,IPv6 那条应为诚实的 ok,"+
			"得到 %s(summary=%q)", f.Verdict, f.Summary)
	}
}

// 只有浏览器那一半缺席(本机事实齐全)时,依然不许升格成 ok。
// 「本地看着没问题」不是「没有泄漏」——组合规则的另一半根本没到。
//
// **fixture 必须带上 IPv6DefaultPresent = False**:那正是真机上会把 IPv6 那条
// 推进 ok 分支的输入。留成零值(Unknown)这条测试就只证明了「Unknown 会短路」。
func TestMissingBrowserHalfStillNotChecked(t *testing.T) {
	local := LocalFacts{
		DefaultRouteV4:     InterfaceRef{Name: "utun4", Display: "Unidentified VPN (utun4)"},
		DefaultRouteV6:     InterfaceRef{Err: "no route"},
		IPv6DefaultPresent: tristate.False,
		DNSServers:         []string{"127.0.0.1"},
		DNSServerEgress:    map[string]string{"127.0.0.1": "lo0"},
	}
	rep := Judge(fixedTime(), BrowserReport{}, local)
	for _, f := range rep.Findings {
		switch f.ID {
		case FindingDNS:
			// 只用本机那一半,由 Task 6 单独覆盖。
		case FindingIPv6:
			// **本机确知没有通往 IPv6 互联网的路,这条就判得了** —— 而且判得对:
			// 没有路就漏不出去,回声说什么都不改变它。要求它也变成 not checked,
			// 等于让一条完全由本机证据支撑的结论去等一个它不需要的东西,而 IPv6
			// 那行会在**每一台关掉 v6 的机器上恒为「没查」** —— 恒绿的镜像。
			if f.Verdict != OK {
				t.Errorf("确知没有 v6 通路时应当给出本机可支撑的 ok,得到 %s(%q)", f.Verdict, f.Summary)
			}
		default:
			if f.Verdict != NotChecked {
				t.Errorf("%s 缺了浏览器那一半就必须是 not checked,得到 %s", f.ID, f.Verdict)
			}
		}
	}
}

// **反过来的那一半:本机有 v6 通路时,缺了浏览器就绝不许说 ok。**
//
// 上一条放宽了「确知没有 v6」那一支,这一条守住它没有被放宽过头 —— 机器上有
// v6 默认路由时,漏没漏只有把两半对起来才知道,而浏览器那半根本没到。
func TestIPv6StaysUncheckedWhenTheMachineHasV6ButTheBrowserNeverRan(t *testing.T) {
	for _, present := range []tristate.Tristate{tristate.True, tristate.Unknown} {
		local := LocalFacts{
			DefaultRouteV4:     InterfaceRef{Name: "en0"},
			DefaultRouteV6:     InterfaceRef{Name: "en0"},
			IPv6DefaultPresent: present,
		}
		rep := Judge(fixedTime(), BrowserReport{}, local)
		for _, f := range rep.Findings {
			if f.ID != FindingIPv6 {
				continue
			}
			if f.Verdict != NotChecked {
				t.Errorf("IPv6DefaultPresent=%v 且浏览器没到过时必须是 not checked,得到 %s(%q)",
					present, f.Verdict, f.Summary)
			}
		}
	}
}

// 结论的 ID 与顺序是页面与 CLI 共同依赖的契约,固定次序好让输出可 diff。
func TestFindingIDsAndOrderAreStable(t *testing.T) {
	rep := Judge(fixedTime(), BrowserReport{}, LocalFacts{})
	// 顺序即分区顺序:流量路径三条在前,身份段在后。页面按这个顺序摆行。
	want := []string{FindingWebRTC, FindingIPv6, FindingDNS, FindingLocalAddresses}
	if len(rep.Findings) != len(want) {
		t.Fatalf("结论条数应为 %d,得到 %d", len(want), len(rep.Findings))
	}
	for i, id := range want {
		if rep.Findings[i].ID != id {
			t.Errorf("第 %d 条应是 %q,得到 %q", i, id, rep.Findings[i].ID)
		}
		if rep.Findings[i].Title == "" {
			t.Errorf("%s 必须有标题", id)
		}
	}
}

// 报告必须原样带出要联系的第三方,页面才能照原样显示(设计风险三)。
func TestJudgeCarriesEndpointDisclosure(t *testing.T) {
	rep := Judge(fixedTime(), BrowserReport{}, LocalFacts{})
	if rep.Endpoints != Endpoints() {
		t.Fatalf("报告必须带出 Endpoints(),得到 %+v", rep.Endpoints)
	}
}
