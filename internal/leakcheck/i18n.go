package leakcheck

import (
	"fmt"
	"strings"
)

// Lang 是页面要的语言。**只有页面那条路会翻**:`bx leak-check` 的 JSON 与 CLI 输出保持
// 英文,agent 与脚本在解析它们(与菜单「CLI 保持英文」同一条纪律)。
type Lang string

const (
	LangEN     Lang = "en"
	LangZHHans Lang = "zh-Hans"
)

// ParseLang 把页面递来的 BCP 47 标签折到我们有的两种:zh 开头且不是繁体地区的一律简体
// 中文(zh、zh-CN、zh-Hans、zh-Hans-CN),其余英文。认不出的不猜。
func ParseLang(tag string) Lang {
	t := strings.ToLower(strings.TrimSpace(tag))
	if t == "zh" || strings.HasPrefix(t, "zh-") {
		if strings.Contains(t, "hant") || strings.HasSuffix(t, "-tw") || strings.HasSuffix(t, "-hk") || strings.HasSuffix(t, "-mo") {
			return LangEN
		}
		return LangZHHans
	}
	return LangEN
}

// sentence 是一句结论:英文原句(可带 %s)+ 参数。
type sentence struct {
	key  string
	args []string
}

// say 记下一条结论句:英文原句是 key(可带 %s),值是参数。Summary 立刻按英文渲染 ——
// Judge 的全部既有测试与 JSON 契约一个字不变;Localize 用同一份 key + 参数按别的语言
// 重渲染。**值(接口名、地址、国家)不翻**:它们是数据,翻了用户反而对不上;但一个值
// 恰好也是表里的英文原句时(「the exit country was not observed」这类固定短语)会一起翻。
func (f *Finding) say(key string, args ...string) {
	f.summary = []sentence{{key: key, args: append([]string(nil), args...)}}
	f.Summary = f.renderSummary(LangEN)
}

// also 在 say 之后再接一句(WebRTC 那条:主句说观察到什么,尾句说这意味着什么)。
func (f *Finding) also(key string, args ...string) {
	f.summary = append(f.summary, sentence{key: key, args: append([]string(nil), args...)})
	f.Summary = f.renderSummary(LangEN)
}

func (f Finding) renderSummary(lang Lang) string {
	parts := make([]string, 0, len(f.summary))
	for _, s := range f.summary {
		key := s.key
		args := s.args
		if lang == LangZHHans {
			if zh, ok := zhHans[key]; ok {
				key = zh
			}
			args = make([]string, len(s.args))
			for i, a := range s.args {
				if zh, ok := zhHansFragments[a]; ok {
					a = zh
				}
				args[i] = a
			}
		}
		parts = append(parts, render(key, args))
	}
	return strings.Join(parts, " ")
}

// tail 标记一句要接在 also 后面的尾句的英文原句(让守卫的正则认得出它是一句结论)。
func tail(key string) string { return key }

func render(key string, args []string) string {
	if len(args) == 0 {
		return key
	}
	any := make([]any, len(args))
	for i, a := range args {
		any[i] = a
	}
	return fmt.Sprintf(key, any...)
}

// Localize 返回按 lang 重渲染标题与结论句的副本;英文与认不出的语言原样返回。
// 没经 say 写的 Summary(没有句子记录)原样保留 —— 不许把一句没在表里的话翻成
// 一句错的。证据行是数据,不翻。
func Localize(r Report, lang Lang) Report {
	if lang != LangZHHans {
		return r
	}
	out := r
	out.Findings = make([]Finding, len(r.Findings))
	for i, f := range r.Findings {
		if zh, ok := zhHans[f.Title]; ok {
			f.Title = zh
		}
		if len(f.summary) > 0 {
			f.Summary = f.renderSummary(lang)
		}
		out.Findings[i] = f
	}
	return out
}

// TitleTranslations 给页面骨架用:英文标题 → 该语言的标题(英文时为空表)。页面不抄
// 第二份译文,拿这一份就够。
func TitleTranslations(lang Lang) map[string]string {
	out := map[string]string{}
	if lang != LangZHHans {
		return out
	}
	for _, c := range Outline() {
		if zh, ok := zhHans[c.Title]; ok {
			out[c.Title] = zh
		}
	}
	return out
}

// zhHans:英文原句 → 简体中文。守卫 TestEveryLeakSentenceHasAChineseTranslation 钉住
// 每一处 say / Title 都在、%s 数目相同、没有陈旧条目。
var zhHans = map[string]string{
	"Who carries your traffic":  "谁在承载你的流量",
	"WebRTC vs HTTP exit":       "WebRTC 与 HTTP 出口",
	"IPv6 exposure":             "IPv6 暴露",
	"DNS path":                  "DNS 路径",
	"Routes around the tunnel":  "绕开隧道的路由",
	"Local network addresses":   "本地网络地址",
	"Clock vs exit location":    "时钟与出口所在地",
	"Language vs exit location": "语言与出口所在地",
	"Fingerprint defences":      "指纹防护",
	"What sites can read":       "网站能读到什么",
	"claude.ai":                 "claude.ai",
	"Anthropic API":             "Anthropic API",
	"OpenAI API":                "OpenAI API",
	"Google AI API":             "Google AI API",
	"Not checked: it could not be determined which interface public traffic leaves through.":                                                                                                                                                                 "未检查:无法确定公网流量从哪块网卡出去。",
	"bx is in a transitional state (%s), so whether it should be carrying this traffic cannot be judged yet.":                                                                                                                                                "bx 正处于过渡状态(%s),此刻还判断不了它该不该承载这些流量。",
	"Your public traffic is carried by bx (%s).":                                                                                                                                                                                                             "你的公网流量正经由 bx(%s)。",
	"No tunnel is carrying this machine's traffic — it leaves directly through %s.":                                                                                                                                                                          "没有任何隧道在承载这台机器的流量 —— 它直接从 %s 出去。",
	"Your public traffic is carried by %s, which bx is not managing. No sign of bx carrying traffic on this machine.":                                                                                                                                        "你的公网流量正经由 %s,而它不归 bx 管。这台机器上看不到 bx 在承载流量的迹象。",
	"bx is running%s, but public traffic is not entering its tunnel at all — it leaves directly through %s. Whatever bx reports about itself, this traffic is not protected.":                                                                                "bx 在运行%s,但公网流量根本没有进它的隧道 —— 而是直接从 %s 出去。不管 bx 自己怎么报告,这些流量没有受保护。",
	"bx is running%s, but your public traffic is carried by %s instead. Whatever bx reports about itself, it is not the one carrying this traffic.":                                                                                                          "bx 在运行%s,但你的公网流量正经由 %s。不管 bx 自己怎么报告,承载这些流量的不是它。",
	"Not checked: the browser's ICE gathering failed (%s), so it never reported any local addresses.":                                                                                                                                                        "未检查:浏览器的 ICE 收集失败(%s),因此没有报告任何本地地址。",
	"Not checked: the browser reported no local addresses at all, so nothing can be said about whether it hides them.":                                                                                                                                       "未检查:浏览器完全没有报告本地地址,无法判断它是否隐藏了它们。",
	"Any website you open can read this machine's address on your local network: %s. That address stays the same across sites and across sessions, so it can be used to recognise you.":                                                                      "你打开的任何网站都能读到这台机器在本地网络里的地址:%s。这个地址跨网站、跨会话都不变,可以用来认出你。",
	"Your browser replaced this machine's local addresses with random .local names, so websites cannot read them.":                                                                                                                                           "浏览器把这台机器的本地地址换成了随机的 .local 名字,网站读不到它们。",
	"Not checked: %s, so there is nothing to compare the clock against.":                                                                                                                                                                                     "未检查:%s,没有东西可以拿来和时钟比对。",
	"Not checked: neither the browser nor this machine reported a time zone.":                                                                                                                                                                                "未检查:浏览器和这台机器都没有报告时区。",
	"Not checked: this exit looks like it is in %s, and bx does not carry a time-zone region for that country, so the two cannot be compared.":                                                                                                               "未检查:这个出口看起来在 %s,而 bx 没有那个国家的时区区域数据,两者无法比对。",
	"This browser's clock (%s) is in a time-zone region that fits an exit in %s. Only the region was compared, so a closer mismatch inside it would not show up here.":                                                                                       "这个浏览器的时钟(%s)所在的时区区域与 %s 的出口相符。只比对了区域,区域内部更细的不一致这里看不出来。",
	"Your traffic leaves in %s, but this browser's clock is set to %s. Any site can read both, and the combination is unusual enough to single you out even though nothing leaked.":                                                                          "你的流量从 %s 出去,但这个浏览器的时钟设在 %s。任何网站都能读到这两样,这个组合足够少见,即使什么都没泄漏也能把你单独认出来。",
	"Not checked: %s, so there is nothing to compare the language against.":                                                                                                                                                                                  "未检查:%s,没有东西可以拿来和语言比对。",
	"Not checked: neither the browser nor this machine reported a language.":                                                                                                                                                                                 "未检查:浏览器和这台机器都没有报告语言。",
	"Not checked: this exit looks like it is in %s, and bx does not carry a language list for that country, so the two cannot be compared.":                                                                                                                  "未检查:这个出口看起来在 %s,而 bx 没有那个国家的语言列表,两者无法比对。",
	"Your browser asks for English, which is unremarkable from any exit.":                                                                                                                                                                                    "你的浏览器请求英文,从任何出口看都不显眼。",
	"Your browser asks for %s, which fits an exit in %s.":                                                                                                                                                                                                    "你的浏览器请求 %s,与 %s 的出口相符。",
	"Your browser asks for %s while your traffic leaves from %s. Nothing leaked, but that combination is uncommon, and it separates you from the other people using this exit. Adding the local language to your browser's language list makes it blend in.": "你的浏览器请求 %s,而你的流量从 %s 出去。没有泄漏,但这个组合不常见,会把你和使用这个出口的其他人区分开。把当地语言加进浏览器的语言列表就能混在一起。",
	"Not checked: the browser half of this check never arrived. The page was never run to completion — the tab was closed, the browser never opened, or the check timed out. Nothing was contacted, so nothing can be concluded.":                            "未检查:这项检查的浏览器那一半从未到达。页面没有跑完 —— 标签页被关了、浏览器没打开,或者检查超时。什么都没有连过,所以什么结论都下不了。",
	"WebRTC could not be checked: %s":                                          "WebRTC 没能检查:%s",
	"WebRTC could not be checked: no server-reflexive candidate was returned.": "WebRTC 没能检查:没有返回任何 server-reflexive 候选地址。",
	"WebRTC could not be compared: %s":                                         "WebRTC 没能比对:%s",
	"WebRTC could not be compared: the IPv4 echo answered with %s, which is not an IP address — the response did not come from the echo service (a captive portal or an error page, most likely).": "WebRTC 没能比对:IPv4 回显服务答的是 %s,不是一个 IP 地址 —— 这个回应不是回显服务给的(多半是强制门户或错误页)。",
	"WebRTC reached the internet from %s, but HTTP traffic left from %s.": "WebRTC 从 %s 到达了互联网,而 HTTP 流量从 %s 出去。",
	"bx is configured to send UDP through a separate tunnel (udp.transport), so WebRTC leaving from a different address is expected — but bx cannot tell that tunnel's exit apart from your real address, so this cannot be settled either way. To get a definitive answer, remove udp.transport and check again.": "bx 配置了把 UDP 送进另一条隧道(udp.transport),所以 WebRTC 从另一个地址出去是预期之内 —— 但 bx 分不清那条隧道的出口和你的真实地址,这一项两边都下不了结论。要一个确定的答案,去掉 udp.transport 再检查一次。",
	"This is what udp.mode=direct-realtime does: it sends all UDP (including QUIC and WebRTC) straight out with your real address, trading anonymity for latency.":                                                                                                                                                 "这正是 udp.mode=direct-realtime 的行为:所有 UDP(包括 QUIC 与 WebRTC)都用你的真实地址直接出去,用匿名换延迟。",
	"WebRTC is bypassing the tunnel.": "WebRTC 绕开了隧道。",
	"WebRTC is bypassing %s.":         "WebRTC 绕开了 %s。",
	"Those are two different ways out of this machine — whichever one you believe you are using, the other one is also reachable.": "这是这台机器的两条不同出路 —— 不管你以为自己用的是哪条,另一条也通着。",
	"WebRTC and HTTP both left from %s, through %s.":                                                                        "WebRTC 与 HTTP 都从 %s 出去,经由 %s。",
	"There is no tunnel to bypass: WebRTC and HTTP both left from %s, which is this machine's own address on the internet.": "没有隧道可绕:WebRTC 与 HTTP 都从 %s 出去,那是这台机器自己在互联网上的地址。",
	"WebRTC and HTTP both left from %s, but it could not be determined whether any tunnel is carrying this machine's traffic — so this agreement does not by itself mean anything is protected.": "WebRTC 与 HTTP 都从 %s 出去,但无法确定是否有任何隧道在承载这台机器的流量 —— 两者一致本身不代表有什么受了保护。",
	"This machine has no route to the IPv6 internet, so nothing can leak over IPv6.":                                                                                                             "这台机器没有通往 IPv6 互联网的路由,不会有东西经 IPv6 泄漏。",
	"IPv6 could not be judged: the local IPv4 default route was not observed.":                                                                                                                   "IPv6 没能判断:没有观测到本机的 IPv4 默认路由。",
	"This machine has no IPv6 default route and no IPv6 exit was observed, so there is no IPv6 path to leak through.":                                                                            "这台机器没有 IPv6 默认路由,也没有观测到 IPv6 出口,没有可供泄漏的 IPv6 路径。",
	"IPv6 could not be checked: %s": "IPv6 没能检查:%s",
	"IPv6 could not be checked: the IPv6 echo answered with %s, which is not an IPv6 address — the browser did not use IPv6.": "IPv6 没能检查:IPv6 回显服务答的是 %s,不是一个 IPv6 地址 —— 浏览器没有用 IPv6。",
	"IPv6 leaves through the same interface as IPv4 (%s).":                                                                    "IPv6 与 IPv4 从同一块网卡出去(%s)。",
	"A public IPv6 exit was observed (%s) but the local IPv6 default route was not observed, so it cannot be attributed.":     "观测到了公网 IPv6 出口(%s),但没有观测到本机的 IPv6 默认路由,无法归因。",
	"IPv4 leaves through %s and IPv6 through %s — two different interfaces, but bx found no tunnel on the IPv4 path, so it cannot say IPv6 is bypassing anything. An ordinary dual-homed machine (wired IPv4, Wi-Fi IPv6) looks exactly like this.": "IPv4 从 %s 出去、IPv6 从 %s 出去 —— 两块不同的网卡,但 bx 在 IPv4 那条路上没有发现隧道,所以说不上 IPv6 绕开了什么。一台普通的双宿主机器(有线 IPv4、Wi-Fi IPv6)就是这个样子。",
	"IPv4 leaves through %s but IPv6 reached the internet as %s through %s. IPv6 is bypassing the tunnel.":                                                                                                                                          "IPv4 从 %s 出去,但 IPv6 以 %s 经 %s 到达了互联网。IPv6 绕开了隧道。",
	"DNS could not be checked: %s":                                        "DNS 没能检查:%s",
	"DNS could not be checked: no resolver was observed on this machine.": "DNS 没能检查:这台机器上没有观测到解析器。",
	"DNS could not be judged: the local IPv4 default route was not observed, so there is nothing to compare the resolvers against.": "DNS 没能判断:没有观测到本机的 IPv4 默认路由,没有东西可以拿来和解析器比对。",
	"Resolver(s) %s are reached outside %s, which owns the default route. DNS queries may bypass the tunnel.":                       "解析器 %s 是在 %s 之外到达的,而默认路由归后者。DNS 查询可能绕开隧道。",
	"DNS could not be fully checked: the egress interface of %s was not observed.":                                                  "DNS 没能完整检查:没有观测到 %s 的出口网卡。",
	"All resolvers (%s) are reached through %s or the local machine.":                                                               "所有解析器(%s)都经 %s 或本机到达。",
	"Not checked: the routing table could not be read (%s).":                                                                        "未检查:读不到路由表(%s)。",
	"Not checked: the routing table was not read.":                                                                                  "未检查:没有读取路由表。",
	"Not checked: no tunnel is carrying this machine's traffic, so there is nothing for a route to go around.":                      "未检查:没有隧道在承载这台机器的流量,路由无所谓绕不绕。",
	"Something has installed routes that send whole ranges of the internet around the tunnel: %s. Traffic to those addresses leaves with your real address. A hostile DHCP server on this network can do exactly this (CVE-2024-3661).": "有东西装了把互联网的整段地址绕开隧道的路由:%s。发往那些地址的流量带着你的真实地址出去。这个网络上一台恶意的 DHCP 服务器正好能做到这一点(CVE-2024-3661)。",
	"No route sends whole ranges of the internet around the tunnel.":          "没有路由把互联网的整段地址绕开隧道。",
	"Not checked: the browser did not return two canvas readings to compare.": "未检查:浏览器没有返回两次 canvas 读数可供比对。",
	"This browser drew the same canvas twice and got the same result both times, so it is not defending against fingerprinting. Sites can build a stable id for this machine and recognise it across visits — no IP address needed.": "这个浏览器画了两次同样的 canvas,两次结果一样,说明它没有防指纹。网站可以给这台机器建一个稳定的标识,跨访问认出它 —— 不需要 IP 地址。",
	"This browser returned a different canvas each time it was asked, which is what anti-fingerprinting defences do.":                                                                                                                "这个浏览器每次被问都返回不同的 canvas,这正是防指纹机制的做法。",
	"Not checked: the browser reported none of these.": "未检查:浏览器一项都没有报告。",
	"Every site you open can read these without asking. None of them is good or bad on its own; together they narrow down which machine you are.": "你打开的每个网站都能不经询问读到这些。单看每一项无所谓好坏;合在一起它们会缩小「你是哪台机器」的范围。",
	"Reachability to %s was not checked in this run.": "本次没有检查 %s 的可达性。",
	"bx can reach %s.": "bx 能连上 %s。",
	"%s has refused connections from the region this exit is in.": "%s 拒绝了来自这个出口所在地区的连接。",
	"This path could not reach %s.":                               "这条路径连不上 %s。",
	"This looks like Cloudflare's bot-verification challenge, not a problem with your exit — a browser can get through this even though the command line cannot.": "这看起来是 Cloudflare 的人机验证,不是你的出口有问题 —— 浏览器能过去,命令行过不去。",
	"bx could not determine whether %s is reachable — this path returned something that wasn't recognized as either reachable or blocked.":                        "bx 无法确定 %s 是否可达 —— 这条路径返回的东西既认不出是可达也认不出是被挡。",
	"It is reachable directly too.": "直连也能到。",
	"It is reachable through your current path but not directly — the tunnel is what gets you there.":                                   "经当前路径能到,直连不能 —— 是隧道把你带过去的。",
	"bx can reach it directly, without the tunnel — so what fails is this path (most likely your tunnel's exit), not your own network.": "bx 不经隧道直连能到 —— 所以坏的是这条路径(多半是你隧道的出口),不是你自己的网络。",
	"bx could not reach it directly either.": "bx 直连也连不上。",
}

// zhHansFragments:作为**参数**递进句子里的固定英文短语(judge_identity 的 reason 那类),
// 渲染时若参数恰好等于表里的原句就一起翻;值(接口名、地址)不会命中。
var zhHansFragments = map[string]string{
	"the exit country was not observed": "没有观测到出口所在国家",
}
