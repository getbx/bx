package leakcheck

import (
	"strings"
	"time"
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

func judgeIPv6(browser BrowserReport, local LocalFacts) Finding {
	return Finding{
		ID:      FindingIPv6,
		Title:   "IPv6 exposure",
		Verdict: NotChecked,
		Summary: "No browser report was received.",
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
