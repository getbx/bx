package leakcheck

import (
	"net/netip"
	"strings"
)

// judgeLocalAddresses 判「这台机器在局域网里的地址,网站看不看得见」。
//
// 数据来自 ICE gathering 的 `typ host` candidate —— **原设计采到了却丢掉了**。
// 丢掉是有道理的:2019 年之后的浏览器把 host candidate 换成随机的
// `<uuid>.local`,拿它当泄漏判据会得到一条恒绿的检查。但换个问法就不恒绿了 ——
// **问「有没有被换掉」**:没换掉的那台机器,内网网段、设备在网内的位置,对每一个
// 打开的网站可见,而那是一条长期稳定、跨站一致的标识。
//
// 这条属于身份段:它不影响流量从哪儿出去,bx 也修不了它(那是浏览器设置)。
// 但它确实能把人钉住,而用户有权知道。
func judgeLocalAddresses(browser BrowserReport) Finding {
	f := Finding{ID: FindingLocalAddresses, Title: "Local network addresses", Section: SectionIdentity}
	if browser.Silent() {
		return browserNeverArrived(f)
	}
	if browser.STUNErr != "" {
		f.Summary = "Not checked: the browser's ICE gathering failed (" + browser.STUNErr +
			"), so it never reported any local addresses."
		f.Evidence = append(f.Evidence, "ice error: "+browser.STUNErr)
		return f
	}
	if len(browser.HostCandidates) == 0 {
		// **「没拿到」不是「没暴露」。** 浏览器可能压根没给 host candidate。
		f.Summary = "Not checked: the browser reported no local addresses at all, " +
			"so nothing can be said about whether it hides them."
		return f
	}

	var exposed []string
	for _, candidate := range browser.HostCandidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		// mDNS 遮蔽后的形状是 `<uuid>.local` —— 不是地址,正是重点。
		if _, err := netip.ParseAddr(candidate); err != nil {
			continue
		}
		exposed = append(exposed, candidate)
	}

	f.Evidence = append(f.Evidence,
		"local candidates: "+strings.Join(browser.HostCandidates, ", ")+"  via "+STUNURL)

	if len(exposed) > 0 {
		f.Verdict = Bad
		f.Summary = "Any website you open can read this machine's address on your local " +
			"network: " + strings.Join(exposed, ", ") + ". That address stays the same " +
			"across sites and across sessions, so it can be used to recognise you."
		return f
	}
	f.Verdict = OK
	f.Summary = "Your browser replaced this machine's local addresses with random " +
		".local names, so websites cannot read them."
	return f
}
