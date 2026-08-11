package leakcheck

import "time"

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

func judgeWebRTC(browser BrowserReport, local LocalFacts) Finding {
	return Finding{
		ID:      FindingWebRTC,
		Title:   "WebRTC vs HTTP exit",
		Verdict: NotChecked,
		Summary: "No browser report was received.",
	}
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
