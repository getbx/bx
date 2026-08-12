package leakcheck

import "strings"

// judgeFingerprint 问的是**「你的浏览器有没有在防」**,不是「你的指纹是什么」。
//
// 「唯一性」这个数算不出来:没有语料库就没有分母,而编一个百分比比不报更糟 ——
// 它看起来最像一个结论,却完全是虚构的。「防没防」反过来是能观测的:Brave / Tor
// 每次会话给 canvas 读回加噪,同一次渲染两遍结果不同;普通浏览器两遍逐字节相同。
func judgeFingerprint(browser BrowserReport) Finding {
	f := Finding{ID: FindingFingerprint, Title: "Fingerprint defences", Section: SectionIdentity}
	if browser.Silent() {
		return browserNeverArrived(f)
	}
	if browser.CanvasA == "" || browser.CanvasB == "" {
		f.Summary = "Not checked: the browser did not return two canvas readings to compare."
		return f
	}
	if browser.CanvasA == browser.CanvasB {
		f.Verdict = Bad
		f.Summary = "This browser drew the same canvas twice and got the same result both " +
			"times, so it is not defending against fingerprinting. Sites can build a stable " +
			"id for this machine and recognise it across visits — no IP address needed."
		f.Evidence = append(f.Evidence, "canvas read twice: identical")
		return f
	}
	f.Verdict = OK
	f.Summary = "This browser returned a different canvas each time it was asked, " +
		"which is what anti-fingerprinting defences do."
	f.Evidence = append(f.Evidence, "canvas read twice: different")
	return f
}

// judgeSurface 只列举网站读得到什么。**永远是 Info。**
//
// 屏幕分辨率、显卡型号、UA 既不好也不坏 —— 判成 ok 会被读成「安全」,判成 bad 会
// 被读成「有问题」,而两者都是在给一件事强加一个它没有的极性。它也不进任何异常
// 计数,所以将来往这里加多少条,都不会把真正的告警稀释掉。
func judgeSurface(browser BrowserReport) Finding {
	f := Finding{ID: FindingSurface, Title: "What sites can read", Section: SectionSurface}
	if browser.Silent() {
		return browserNeverArrived(f)
	}
	add := func(label, value string) {
		if strings.TrimSpace(value) != "" {
			f.Evidence = append(f.Evidence, label+": "+value)
		}
	}
	add("user agent", browser.UserAgent)
	add("screen", browser.Screen)
	add("languages", strings.Join(browser.Languages, ", "))
	add("time zone", browser.Timezone)
	add("cpu cores", browser.HardwareConcurrency)
	add("device memory (GB)", browser.DeviceMemory)
	add("gpu", browser.WebGLRenderer)

	if len(f.Evidence) == 0 {
		// 一个字都没采到时不摆空壳 —— 那是刚从菜单里删掉的占位符换个地方回来。
		f.Summary = "Not checked: the browser reported none of these."
		return f
	}
	f.Verdict = Info
	f.Summary = "Every site you open can read these without asking. None of them is good " +
		"or bad on its own; together they narrow down which machine you are."
	return f
}
