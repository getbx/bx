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

// judgeTimezone 判「你的时钟与你的出口自相矛盾」。
//
// **这是这个页面唯一一类 browserleaks 做不到的判断。** 它会告诉你
// `Timezone: Asia/Shanghai`,但它不知道你的出口在哪儿,所以说不出这两件事互相
// 打架 —— 而后者才是能把人钉住的那件事:一个出口在洛杉矶、时钟在上海的浏览器,
// IP 一个字节都没漏,人已经被标出来了。
//
// **判据只做到大区(IANA 时区名的第一段)这一层**,而且 ok 的措辞必须说清楚这一点:
// 出口在美国、时钟在 America/Sao_Paulo 是同一个大区而显然仍是矛盾。一句
// 「一切正常」会让用户以为这件事已经查完了。
func judgeTimezone(browser BrowserReport) Finding {
	f := Finding{ID: FindingTimezone, Title: "Clock vs exit location", Section: SectionIdentity}
	if browser.Silent() {
		return browserNeverArrived(f)
	}

	country := strings.ToUpper(strings.TrimSpace(browser.ExitCountry))
	zone := strings.TrimSpace(browser.Timezone)
	switch {
	case country == "":
		reason := browser.TraceErr
		if reason == "" {
			reason = "the exit country was not observed"
		}
		f.Summary = "Not checked: " + reason + ", so there is nothing to compare the clock against."
		f.Evidence = append(f.Evidence, "trace: "+TraceURL)
		return f
	case zone == "":
		f.Summary = "Not checked: the browser did not report a time zone."
		return f
	}

	areas, known := countryTimeAreas[country]
	if !known {
		// **认不出的国家必须说认不出。** 编一个大区去比,得到的是一条随机的红或绿。
		f.Summary = "Not checked: this exit looks like it is in " + country +
			", and bx does not carry a time-zone region for that country, so the two " +
			"cannot be compared."
		f.Evidence = append(f.Evidence, "exit country: "+country, "browser time zone: "+zone)
		return f
	}

	area, _, _ := strings.Cut(zone, "/")
	f.Evidence = append(f.Evidence,
		"browser time zone: "+zone,
		"exit country: "+country+"  via "+TraceURL,
		"time-zone regions valid for "+country+": "+strings.Join(areas, ", "))

	for _, valid := range areas {
		if area == valid {
			f.Verdict = OK
			f.Summary = "This browser's clock (" + zone + ") is in a time-zone region that " +
				"fits an exit in " + country + ". Only the region was compared, so a closer " +
				"mismatch inside it would not show up here."
			return f
		}
	}
	f.Verdict = Bad
	f.Summary = "Your traffic leaves in " + country + ", but this browser's clock is set to " +
		zone + ". Any site can read both, and the combination is unusual enough to single " +
		"you out even though nothing leaked."
	return f
}

// judgeLanguage 拿浏览器报的语言与出口国比。**与 judgeTimezone 是同一类**:
// 一个字节都没漏,也能把你和这个出口的其余用户分开。
//
// ## 判据刻意宽松,而且只在能证明时才判
//
//   - 出口国问不出来 → 不判(与「国旗只在能证明时给」同一条纪律)
//   - 表里没有那个国家 → 不判,并说明是 bx 不带那份数据,不是用户有问题
//   - **英语一律放过**:它是网上的通用语,从任何出口报 en 都不扎眼。把它判成
//     不符会让一大批完全正常的人天天看到红字,而红字看多了就没人看了。
//
// ## 为什么它进身份段而不是 surface 段
//
// surface 段是「网站看得到什么」,中性、不判 —— 语言本身确实属于那里(它已经
// 在那儿列着了)。但**语言与出口国不符**不是一个中性事实:它是一条把你从这个
// 出口的其他人里挑出来的线索,和时钟那条完全同型。
func judgeLanguage(browser BrowserReport) Finding {
	f := Finding{ID: FindingLanguage, Title: "Language vs exit location", Section: SectionIdentity}
	if browser.Silent() {
		return browserNeverArrived(f)
	}
	country := strings.ToUpper(strings.TrimSpace(browser.ExitCountry))
	primary := primaryLanguage(browser.Languages)
	switch {
	case country == "":
		reason := browser.TraceErr
		if reason == "" {
			reason = "the exit country was not observed"
		}
		f.Summary = "Not checked: " + reason + ", so there is nothing to compare the language against."
		return f
	case primary == "":
		f.Summary = "Not checked: the browser did not report a language."
		return f
	}
	expected, known := countryLanguages[country]
	if !known {
		f.Summary = "Not checked: this exit looks like it is in " + country +
			", and bx does not carry a language list for that country, so the two cannot be compared."
		f.Evidence = append(f.Evidence, "exit country: "+country, "browser language: "+primary)
		return f
	}
	f.Evidence = append(f.Evidence, "exit country: "+country, "browser language: "+primary)
	// **英语一律放过** —— 见上面的说明。
	if primary == "en" {
		f.Verdict = OK
		f.Summary = "Your browser asks for English, which is unremarkable from any exit."
		return f
	}
	for _, want := range expected {
		if primary == want {
			f.Verdict = OK
			f.Summary = "Your browser asks for " + primary + ", which fits an exit in " + country + "."
			return f
		}
	}
	f.Verdict = Bad
	f.Summary = "Your browser asks for " + primary + " while your traffic leaves from " + country +
		". Nothing leaked, but that combination is uncommon, and it separates you from the other " +
		"people using this exit. Adding the local language to your browser's language list makes it blend in."
	return f
}

// primaryLanguage 取第一条语言的**主标签**(zh-CN → zh)。
//
// 只看第一条:那是网站真正拿去决定内容的那个,而后面几条是回落。
func primaryLanguage(languages []string) string {
	for _, raw := range languages {
		tag := strings.TrimSpace(raw)
		if tag == "" {
			continue
		}
		if i := strings.IndexAny(tag, "-_"); i > 0 {
			tag = tag[:i]
		}
		return strings.ToLower(tag)
	}
	return ""
}
