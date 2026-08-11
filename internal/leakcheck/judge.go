package leakcheck

import (
	"net/netip"
	"strings"
	"time"

	"github.com/getbx/bx/internal/tristate"
)

// 三条结论的 ID。页面与 CLI 都按它们取,固定不变。
const (
	FindingWebRTC = "webrtc_srflx"
	FindingIPv6   = "ipv6_leak"
	FindingDNS    = "dns_path"
)

// Judge 把两半事实对起来,产出一组三态结论。**纯函数**:同样的输入永远同样的
// 输出,时间也是传进来的。
//
// 结论只有三条,而且每一条都能真的判成 bad。**刻意没有「出口位置」「默认路由
// 归谁」这两条**:它们在真机上永远是 ok(没有任何输入能让它们变坏),而一条
// 恒绿的检查会把整个界面训练成装饰(设计风险二)。它们作为 evidence 出现,
// 用户照样看得到,只是不冒充结论。
func Judge(now time.Time, browser BrowserReport, local LocalFacts) Report {
	findings := []Finding{
		judgeWebRTC(browser, local),
		judgeIPv6(browser, local),
		judgeDNS(browser, local),
	}
	return NewReport(now, Endpoints(), findings, collectEvidence(browser, local))
}

// judgeWebRTC 是这个功能的立身之本那条:STUN 问出来的 server-reflexive 地址
// 与 HTTP 出口地址不是同一个 → WebRTC 走了隧道之外的路。
//
// 两半都必须在场才比较。**「没拿到 candidate」不是「没有泄漏」** —— STUN 被挡、
// UDP 被完全阻断、页面被关掉,全部停在 not checked。
func judgeWebRTC(browser BrowserReport, local LocalFacts) Finding {
	f := Finding{ID: FindingWebRTC, Title: "WebRTC vs HTTP exit"}

	// 顺序刻意是「先说没问出来,再说好坏」。任何一半缺席都到不了比较那一步。
	switch {
	case browser.STUNErr != "":
		f.Summary = "WebRTC could not be checked: " + browser.STUNErr
		f.Evidence = append(f.Evidence, "stun: "+STUNURL, "stun error: "+browser.STUNErr)
		return f
	case len(browser.SRFLX) == 0:
		// ICE 跑完了但一个 srflx 都没有(常见于 UDP 被完全阻断)。
		// 「没拿到」不是「没泄漏」。
		f.Summary = "WebRTC could not be checked: no server-reflexive candidate was returned."
		f.Evidence = append(f.Evidence, "stun: "+STUNURL)
		return f
	case browser.ExitV4 == "":
		reason := browser.ExitV4Err
		if reason == "" {
			reason = "the HTTP exit address was not observed"
		}
		f.Summary = "WebRTC could not be compared: " + reason
		f.Evidence = append(f.Evidence, "srflx: "+strings.Join(browser.SRFLX, ", "), "echo v4: "+EchoV4URL)
		return f
	}

	f.Evidence = append(
		f.Evidence,
		"http exit (v4): "+browser.ExitV4+"  via "+EchoV4URL,
		"webrtc srflx: "+strings.Join(browser.SRFLX, ", ")+"  via "+STUNURL,
	)
	var mismatched []string
	for _, candidate := range browser.SRFLX {
		if candidate != browser.ExitV4 {
			mismatched = append(mismatched, candidate)
		}
	}
	if len(mismatched) > 0 {
		f.Verdict = Bad
		f.Summary = "WebRTC reached the internet from " + strings.Join(mismatched, ", ") +
			", but HTTP traffic left from " + browser.ExitV4 + ". WebRTC is bypassing the tunnel."
		return f
	}
	f.Verdict = OK
	f.Summary = "WebRTC and HTTP both left from " + browser.ExitV4 + "."
	return f
}

// judgeIPv6 判「v4 走 VPN 而 v6 出口是 ISP」——别的 VPN 最常见的那个漏洞。
//
// **有一个坑必须先过**:v6-only 主机名**不保证浏览器真的用了 IPv6**。在 fake-IP
// DNS 之下(bx 自己就是,别的同类 VPN 也是),`ipv6.icanhazip.com` 会被答一个
// A 记录,请求实际经隧道走 v4 出去,回声返回的是一个 **IPv4 字面量**。本机实测:
// 开着 bx 时 `dig ipv6.icanhazip.com` 返回 `198.18.1.7`(bx 的 fake-IP 池)。
// 所以判据必须先看**回声返回的地址族**;不看的话,一台 bx 工作正常的机器会被
// 判成 v6 泄漏 —— 对自己的用户误报。
func judgeIPv6(browser BrowserReport, local LocalFacts) Finding {
	f := Finding{ID: FindingIPv6, Title: "IPv6 exposure"}

	// 「v4 走 VPN 而 v6 是 ISP」的前半句:v4 归谁必须先知道。
	if !local.DefaultRouteV4.Known() {
		f.Summary = "IPv6 could not be judged: the local IPv4 default route was not observed."
		return f
	}
	f.Evidence = append(f.Evidence, "default route (v4): "+describeRef(local.DefaultRouteV4))

	if browser.ExitV6 == "" {
		// 回声没答上来。这时**本机那一半**决定它是 ok 还是 not checked:
		// 确知没有 v6 默认路由 = 确实没有 v6 通路(诚实的 ok);
		// 有 v6 默认路由、或本机 v6 事实压根没问出来 = 没检查。
		reason := browser.ExitV6Err
		if reason == "" {
			reason = "the IPv6 echo returned nothing"
		}
		switch local.IPv6DefaultPresent {
		case tristate.False:
			f.Verdict = OK
			f.Summary = "This machine has no IPv6 default route and no IPv6 exit was observed, " +
				"so there is no IPv6 path to leak through."
			f.Evidence = append(f.Evidence, "ipv6 default route: none", "echo v6: "+reason)
		default:
			f.Summary = "IPv6 could not be checked: " + reason
			f.Evidence = append(
				f.Evidence,
				"ipv6 default route: "+describeRef(local.DefaultRouteV6),
				"echo v6: "+EchoV6URL,
			)
		}
		return f
	}

	// **fake-IP 的坑**:v6-only 主机名可能被答成 A 记录,回声于是返回一个 v4
	// 字面量。那说明浏览器没走 IPv6,判不了 —— 不是 ok,也不是 bad。
	// v4-mapped(`::ffff:1.2.3.4`)是同一件事换个写法,一并挡在这里;
	// 解析不出来的串(错误页、被中间设备替换的响应)同样不是答案。
	addr, err := netip.ParseAddr(strings.TrimSpace(browser.ExitV6))
	if err != nil || addr.Is4() || addr.Is4In6() {
		f.Summary = "IPv6 could not be checked: the IPv6 echo answered with " +
			browser.ExitV6 + ", which is not an IPv6 address — the browser did not use IPv6."
		f.Evidence = append(f.Evidence, "echo v6 answered: "+browser.ExitV6+"  via "+EchoV6URL)
		return f
	}
	f.Evidence = append(
		f.Evidence,
		"ipv6 exit: "+browser.ExitV6+"  via "+EchoV6URL,
		"default route (v6): "+describeRef(local.DefaultRouteV6),
	)

	// 同一性按**接口名**判,不按展示名:展示名可能两边都翻不出来(空串),
	// 拿它做同一性判断会把两个不同接口判成同一个 → 一条真泄漏被说成 ok。
	if local.DefaultRouteV6.Known() && local.DefaultRouteV6.Name == local.DefaultRouteV4.Name {
		f.Verdict = OK
		f.Summary = "IPv6 leaves through the same interface as IPv4 (" +
			describeRef(local.DefaultRouteV4) + ")."
		return f
	}
	if !local.DefaultRouteV6.Known() {
		f.Summary = "A public IPv6 exit was observed (" + browser.ExitV6 +
			") but the local IPv6 default route was not observed, so it cannot be attributed."
		return f
	}
	f.Verdict = Bad
	f.Summary = "IPv4 leaves through " + describeRef(local.DefaultRouteV4) +
		" but IPv6 reached the internet as " + browser.ExitV6 + " through " +
		describeRef(local.DefaultRouteV6) + ". IPv6 is bypassing the tunnel."
	return f
}

// describeRef 渲染一个接口:能翻成人话就用人话,翻不出就说翻不出。
// 归因规则见 describe.go 的 DescribeInterface —— 这里只负责挑 Display 还是 Name。
func describeRef(ref InterfaceRef) string {
	switch {
	case ref.Display != "":
		return ref.Display
	case ref.Name != "":
		return ref.Name
	case ref.Err != "":
		return "not observed (" + ref.Err + ")"
	default:
		return "not observed"
	}
}

func judgeDNS(browser BrowserReport, local LocalFacts) Finding {
	return Finding{
		ID:      FindingDNS,
		Title:   "DNS path",
		Verdict: NotChecked,
		Summary: "Local DNS facts were not observed.",
	}
}

// collectEvidence 在 Task 7 填内容;现在返回 nil,让报告结构先成立。
func collectEvidence(browser BrowserReport, local LocalFacts) []string { return nil }
