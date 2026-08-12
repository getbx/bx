package leakcheck

import (
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/getbx/bx/internal/tristate"
)

// 三条结论的 ID。页面与 CLI 都按它们取,固定不变。
const (
	FindingWebRTC = "webrtc_srflx"
	FindingIPv6   = "ipv6_leak"
	FindingDNS    = "dns_path"
	// FindingLocalAddresses 属于身份段:它回答的不是「流量去哪了」,
	// 而是「网站能不能看见你这台机器在局域网里的样子」。
	FindingLocalAddresses = "local_addresses"
	// FindingTimezone 同属身份段:时钟与出口矛盾时,一个字节都没漏也能把人钉住。
	FindingTimezone = "timezone_vs_exit"
	// FindingRouteEscape 属于流量路径段,而且是唯一一条能查出**主动攻击**的规则:
	// 别的检查看流量最终从哪儿出去,它看有没有人动了你的路由表。
	FindingRouteEscape = "route_escape"
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
		judgeRouteEscape(local),
		judgeLocalAddresses(browser),
		judgeTimezone(browser),
	}
	return NewReport(now, Endpoints(), findings, collectEvidence(browser, local))
}

// browserNeverArrived 是「浏览器那一半从来没到过」时的结论。**依赖浏览器的两条
// 规则共用它**,免得将来只改一处 —— 两条同时被这件事影响,而它们的失败方式一样坏。
//
// 措辞刻意不提任何观测:一句「no IPv6 exit was observed」在页面从没联网时是假话,
// 而它恰恰是用户唯一会读的那一行(设计风险四)。
func browserNeverArrived(f Finding) Finding {
	f.Verdict = NotChecked
	f.Summary = "Not checked: the browser half of this check never arrived. " +
		"The page was never run to completion — the tab was closed, the browser never " +
		"opened, or the check timed out. Nothing was contacted, so nothing can be concluded."
	f.Evidence = append(f.Evidence, "browser report: never arrived")
	return f
}

// judgeWebRTC 是这个功能的立身之本那条:STUN 问出来的 server-reflexive 地址
// 与 HTTP 出口地址不是同一个 → WebRTC 走了隧道之外的路。
//
// 两半都必须在场才比较。**「没拿到 candidate」不是「没有泄漏」** —— STUN 被挡、
// UDP 被完全阻断、页面被关掉,全部停在 not checked。
func judgeWebRTC(browser BrowserReport, local LocalFacts) Finding {
	f := Finding{ID: FindingWebRTC, Title: "WebRTC vs HTTP exit", Section: SectionPath}
	if browser.Silent() {
		return browserNeverArrived(f)
	}

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

	// **回声端返回的必须先是一个地址。**
	//
	// `fetch` 对 4xx/5xx 不 reject,所以 Cloudflare 的拦截页、强制门户的登录页、
	// 429 的 body 会原样成为 ExitV4 —— 而 bx 的用户在 GFW 后面、三个端点全是
	// Cloudflare 前置的,这不是奇景。拿一张 HTML 去跟 srflx 比必然不等,于是
	// 这条规则会**在一台完全正常的机器上**喊「WebRTC is bypassing the tunnel」。
	//
	// **乱喊狼来了与恒绿是同一条设计风险的两面**(风险二):两者都把这个界面
	// 训练成装饰。judgeIPv6 早就这么挡了,这里此前一处校验都没有。
	exit, err := netip.ParseAddr(strings.TrimSpace(browser.ExitV4))
	if err != nil {
		f.Summary = "WebRTC could not be compared: the IPv4 echo answered with " +
			abbreviate(browser.ExitV4) + ", which is not an IP address — the response " +
			"did not come from the echo service (a captive portal or an error page, most likely)."
		f.Evidence = append(
			f.Evidence,
			"echo v4 answered: "+abbreviate(browser.ExitV4)+"  via "+EchoV4URL,
			"srflx: "+strings.Join(browser.SRFLX, ", "),
		)
		return f
	}
	exitAddr := exit.String()

	f.Evidence = append(
		f.Evidence,
		"http exit (v4): "+exitAddr+"  via "+EchoV4URL,
		"webrtc srflx: "+strings.Join(browser.SRFLX, ", ")+"  via "+STUNURL,
	)
	var mismatched []string
	for _, candidate := range browser.SRFLX {
		if strings.TrimSpace(candidate) != exitAddr {
			mismatched = append(mismatched, candidate)
		}
	}
	// **谁在管这条路,决定了这两半一致时能不能算好消息。**
	owner := WhoOwnsTheRoute(local)
	f.Evidence = append(f.Evidence, "public traffic leaves via: "+describeOwner(owner, local))

	if len(mismatched) > 0 {
		// 不一致仍然判 bad,**与归谁管无关**:两条路出口不同本身就是观测到的事实,
		// 不依赖隧道是否存在。规则是单边的 —— ok 需要前提,bad 不需要。
		f.Verdict = Bad
		f.Summary = "WebRTC reached the internet from " + strings.Join(mismatched, ", ") +
			", but HTTP traffic left from " + exitAddr + ". "

		// **bx 自己的按类分流会长得和泄漏一模一样。**
		//
		// `udp.transport` 指向另一台服务器时,UDP 经隧道出去、只是出口是另一台,
		// 于是 srflx ≠ HTTP 出口 —— 零泄漏。此前这里一律判 bad,对自己的用户
		// 喊狼来了,而「乱喊狼来了与恒绿是同一条设计风险的两面」。
		//
		// **不判 ok 是刻意的**:bx 不知道那台 UDP 服务器的出口地址,所以它分不清
		// 「srflx 是 hysteria 的出口」与「srflx 是用户的真实 IP」。判 ok 就是把
		// 一个假阳性换成假阴性,而后者正是这个功能存在的理由。
		//
		// 只对 OwnerBX 生效:这份配置描述的是 bx 自己那条隧道,拿它去解释别人的
		// VPN 就是张冠李戴,代价是把别人的真实泄漏说成「查不了」。
		if owner == OwnerBX && local.BXUDPTransport != "" {
			f.Verdict = NotChecked
			f.Summary += "bx is configured to send UDP through a separate tunnel " +
				"(udp.transport), so WebRTC leaving from a different address is expected — " +
				"but bx cannot tell that tunnel's exit apart from your real address, " +
				"so this cannot be settled either way. To get a definitive answer, " +
				"remove udp.transport and check again."
			f.Evidence = append(f.Evidence, "bx udp.transport: "+local.BXUDPTransport)
			return f
		}

		switch {
		case owner == OwnerBX && local.BXUDPMode == "direct-realtime":
			// 真的以真实 IP 直连,这就是泄漏 —— 但它是配置选的,不是故障,
			// 措辞要让用户知道该去关哪个开关。
			f.Summary += "This is what udp.mode=direct-realtime does: it sends all UDP " +
				"(including QUIC and WebRTC) straight out with your real address, trading " +
				"anonymity for latency."
			f.Evidence = append(f.Evidence, "bx udp.mode: direct-realtime")
			return f
		}
		switch owner {
		case OwnerBX:
			f.Summary += "WebRTC is bypassing the tunnel."
		case OwnerOther:
			f.Summary += "WebRTC is bypassing the VPN on " + describeRef(local.DefaultRouteV4) + "."
		default:
			f.Summary += "Those are two different ways out of this machine — " +
				"whichever one you believe you are using, the other one is also reachable."
		}
		return f
	}

	switch owner {
	case OwnerBX, OwnerOther:
		f.Verdict = OK
		f.Summary = "WebRTC and HTTP both left from " + exitAddr + ", through " +
			describeOwner(owner, local) + "."
		if owner == OwnerOther {
			// **免责声明进证据区,不进结论句。** 结论句只说这条发现本身;
			// 而「这条隧道不是我建的、我看不到它怎么配的」是关于本工具能力边界的话。
			f.Evidence = append(f.Evidence,
				"this tunnel was not set up by bx, so its configuration is not visible here")
		}
	case OwnerNone:
		// **没有隧道时,「没有绕过隧道」不是好消息。**
		f.Verdict = NotChecked
		f.Summary = "There is no tunnel to bypass: WebRTC and HTTP both left from " +
			exitAddr + ", which is this machine's own address on the internet."
	default:
		f.Verdict = NotChecked
		f.Summary = "WebRTC and HTTP both left from " + exitAddr + ", but it could not be " +
			"determined whether any tunnel is carrying this machine's traffic — so this " +
			"agreement does not by itself mean anything is protected."
	}
	return f
}

// describeOwner 把「谁在管这条路」翻成结论句里能直接用的一截。
//
// OwnerOther 带上接口名:用法二里用户要认出的正是**他自己那条 VPN**,而接口名是
// 这个工具唯一能给出的、属于那条隧道的标识 —— 它是谁家的产品、怎么配的,本工具
// 一无所知,也不假装知道。
func describeOwner(owner TunnelOwner, local LocalFacts) string {
	switch owner {
	case OwnerBX:
		return "the tunnel bx is managing (" + describeRef(local.DefaultRouteV4) + ")"
	case OwnerOther:
		return "the VPN on " + describeRef(local.DefaultRouteV4)
	case OwnerNone:
		return "this machine's own network interface (" + describeRef(local.DefaultRouteV4) + ") — no tunnel"
	default:
		return "an interface that could not be identified"
	}
}

// maxQuotedBody 是把第三方返回的原文引进结论时的上限。回声端本该返回一行地址;
// 走到引用它的那条路上时它已经是别的东西了(拦截页动辄上千字节,而上报体上限
// 是 1MB),整段贴进终端会把结论本身冲掉。
const maxQuotedBody = 120

// abbreviate 把要引进结论的第三方原文截短,并把换行压平。
func abbreviate(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= maxQuotedBody {
		return strconv.Quote(s)
	}
	return strconv.Quote(s[:maxQuotedBody]) + "… (truncated)"
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
	f := Finding{ID: FindingIPv6, Title: "IPv6 exposure", Section: SectionPath}

	// **没有通往 IPv6 互联网的路,就漏不了 IPv6 —— 这一条本机自己就答得了。**
	//
	// 它排在最前面,因为它**不依赖浏览器那一半**:确知没有 v6 默认路由,
	// 就没有任何 v6 流量出得去,回声说什么都不改变这个结论。
	//
	// 此前这条判断埋在「回声没答上来」那一支里,于是回声**答上来了**的时候反而
	// 判不出来:真机(2026-08-11)上 bx 开着、v6 被 reject,而浏览器拿到了一个
	// v6 出口,规则要把它归属到某个本地接口、归不上,就报 not checked ——
	// 一台**根本不可能漏 v6** 的机器上什么都没说。
	//
	// 那个 v6 出口不矛盾,它只是不属于这台机器:v6-only 主机名被 fake-IP DNS 用
	// A 记录答了,浏览器走 v4 进隧道,**服务器**再用它自己的 IPv6 去连回声端。
	// 所以下面把它作为证据**解释掉**,而不是拿它去质疑一个本机已经确定的事实。
	//
	// 措辞只说本机观测到的东西 —— 不提「没看到 v6 出口」,那在浏览器没跑时是
	// 一句断言从未发生的观测的假话(2026-08-11 整枝复审的 Critical 就是它)。
	if local.IPv6DefaultPresent == tristate.False {
		f.Verdict = OK
		f.Summary = "This machine has no route to the IPv6 internet, so nothing can leak over IPv6."
		f.Evidence = append(f.Evidence, "ipv6 default route: none")
		if browser.ExitV6 != "" {
			f.Evidence = append(f.Evidence,
				"ipv6 exit seen by "+EchoV6URL+": "+browser.ExitV6,
				"that address belongs to whatever your traffic passes through, not to this machine — "+
					"this machine has no IPv6 route of its own")
		}
		return f
	}

	// 浏览器那一半从没到过,而本机也没能确定「没有 v6 通路」—— 什么都不能说。
	if browser.Silent() {
		return browserNeverArrived(f)
	}

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
			abbreviate(browser.ExitV6) + ", which is not an IPv6 address — the browser did not use IPv6."
		f.Evidence = append(f.Evidence, "echo v6 answered: "+abbreviate(browser.ExitV6)+"  via "+EchoV6URL)
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
	// **设计的前提是「v4 走 VPN 而 v6 出口是 ISP」,前半句必须自己成立。**
	//
	// 光凭「两个地址族的接口名不一样」就指控,一台**一个 VPN 都没开**的双宿 Mac
	// 就会被判泄漏:插着坞站、v4 默认路由在只有 v4 的公司有线网口、v6 默认路由
	// 在 Wi-Fi —— 那是一台正常机器的样子,没有隧道可绕。
	//
	// 「有没有隧道」只能从手上这点事实推:v4 默认路由那个接口是 bx 自己的 TUN
	// (Guardian 报的),或者它长得像一条隧道设备(IsTunnelInterface)。**推不出
	// 来就说推不出来**,不许把一句判不了的话说成指控 —— 乱喊狼来了与恒绿是同一
	// 条设计风险的两面。
	if !isTunnelPath(local) {
		f.Summary = "IPv4 leaves through " + describeRef(local.DefaultRouteV4) +
			" and IPv6 through " + describeRef(local.DefaultRouteV6) +
			" — two different interfaces, but bx found no tunnel on the IPv4 path, " +
			"so it cannot say IPv6 is bypassing anything. An ordinary dual-homed machine " +
			"(wired IPv4, Wi-Fi IPv6) looks exactly like this."
		return f
	}

	f.Verdict = Bad
	f.Summary = "IPv4 leaves through " + describeRef(local.DefaultRouteV4) +
		" but IPv6 reached the internet as " + browser.ExitV6 + " through " +
		describeRef(local.DefaultRouteV6) + ". IPv6 is bypassing the tunnel."
	return f
}

// isTunnelPath 回答「v4 默认路由上有没有一条隧道」。
//
// bx 自己那条 TUN 按 Guardian 报的名字**确知**;其余按设备名判(IsTunnelInterface)。
// 这是「v4 走 VPN 而 v6 是 ISP」那句前半句唯一能落到的事实。
//
// 头一支在 darwin 上与设备名那一支重合(bx 的 TUN 就叫 utunN),留着是因为它是
// **权威**的那一个:采集层今天只有 macOS,而 bx 在 Linux/Windows 上的 TUN 叫
// `bx0` —— 谁把采集层移过去,这条前半句不该跟着一起失灵。它只会把判定往
// 「是隧道」推,永远不会多产出一条指控。
func isTunnelPath(local LocalFacts) bool {
	switch WhoOwnsTheRoute(local) {
	case OwnerBX, OwnerOther:
		return true
	default:
		return false
	}
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

// judgeDNS 判「默认路由归 X,而 DNS 解析器在 X 之外」。
//
// 措辞是「**可能**绕过」,不是确定的泄漏:解析器在隧道之外确实说明查询明文交给了
// 局域网/ISP,但它是不是用户在意的那种暴露,得由用户看着证据自己判断。
// 把可能说成确定,用户会去追一个不存在的问题,然后学会不信这个界面。
//
// **它为什么不是一条恒绿的检查**(计划给本任务的既定问题):让它变 bad 的真实输入是
// 「只装路由、不改系统 DNS 的第三方 VPN」——`wg-quick` 的 `DNS =` 是可选的,
// Tailscale exit node 关掉 MagicDNS 也是这个形状:默认路由归 utun,而解析器还是
// 局域网路由器,查询明文交给它。这正是本功能瞄准的场景(「别的 VPN 在跑时也能用」)。
// 另一边同样可达:bx 自己接管 DNS 时解析器是 127.0.0.1(loopback ⇒ ok),
// 什么都没开时解析器与默认路由同在 en0(⇒ ok)。三个判定都不是摆设。
func judgeDNS(browser BrowserReport, local LocalFacts) Finding {
	f := Finding{ID: FindingDNS, Title: "DNS path", Section: SectionPath}

	if local.DNSErr != "" {
		f.Summary = "DNS could not be checked: " + local.DNSErr
		f.Evidence = append(f.Evidence, "dns error: "+local.DNSErr)
		return f
	}
	if len(local.DNSServers) == 0 {
		f.Summary = "DNS could not be checked: no resolver was observed on this machine."
		return f
	}
	if !local.DefaultRouteV4.Known() {
		f.Summary = "DNS could not be judged: the local IPv4 default route was not observed, " +
			"so there is nothing to compare the resolvers against."
		f.Evidence = append(f.Evidence, "resolvers: "+strings.Join(local.DNSServers, ", "))
		return f
	}

	route := local.DefaultRouteV4
	f.Evidence = append(f.Evidence, "default route (v4): "+describeRef(route))

	var outside, unknown []string
	for _, server := range local.DNSServers {
		egress, ok := local.DNSServerEgress[server]
		if !ok || egress == "" {
			unknown = append(unknown, server)
			f.Evidence = append(f.Evidence, "resolver "+server+": egress not observed")
			continue
		}
		f.Evidence = append(f.Evidence, "resolver "+server+": via "+egress)
		if egress == route.Name {
			continue
		}
		// 本机 loopback 上的解析器(bx 自己接管 DNS 时就是这个形状)不算绕过:
		// 它根本没离开这台机器,真正出网的是 bx,而 bx 就在默认路由上。
		if isLoopbackResolver(server) {
			continue
		}
		outside = append(outside, server)
	}

	switch {
	case len(outside) > 0:
		f.Verdict = Bad
		f.Summary = "Resolver(s) " + strings.Join(outside, ", ") +
			" are reached outside " + describeRef(route) +
			", which owns the default route. DNS queries may bypass the tunnel."
	case len(unknown) > 0:
		f.Summary = "DNS could not be fully checked: the egress interface of " +
			strings.Join(unknown, ", ") + " was not observed."
	default:
		f.Verdict = OK
		f.Summary = "All resolvers (" + strings.Join(local.DNSServers, ", ") +
			") are reached through " + describeRef(route) + " or the local machine."
	}
	return f
}

// isLoopbackResolver 判断解析器是不是本机。认不出来的地址返回 false ——
// 「看不懂的地址」不能当成安全的那一边。
func isLoopbackResolver(server string) bool {
	addr, err := netip.ParseAddr(strings.TrimSpace(server))
	return err == nil && addr.IsLoopback()
}

// collectEvidence 列出这一轮采到的**原始事实**,两半都在。
//
// 「谁占着默认路由」「公网出口是什么」放在这里而**不是**作为结论行,是因为它们
// 在真机上永远判不出 bad —— 一条恒绿的检查会把整个界面训练成装饰(设计风险二)。
// 放进证据里,用户照样看得到,只是不冒充结论。
func collectEvidence(browser BrowserReport, local LocalFacts) []string {
	value := func(observed, err string) string {
		switch {
		case observed != "":
			return observed
		case err != "":
			return "not observed (" + err + ")"
		default:
			return "not observed"
		}
	}
	evidence := []string{
		"http exit (v4): " + value(browser.ExitV4, browser.ExitV4Err) + "  via " + EchoV4URL,
		"http exit (v6): " + value(browser.ExitV6, browser.ExitV6Err) + "  via " + EchoV6URL,
		"webrtc srflx: " + value(strings.Join(browser.SRFLX, ", "), browser.STUNErr) + "  via " + STUNURL,
		"default route (v4): " + describeRef(local.DefaultRouteV4),
		"default route (v6): " + describeRef(local.DefaultRouteV6),
		"resolvers: " + value(strings.Join(local.DNSServers, ", "), local.DNSErr),
		"browser: " + value(browser.UserAgent, ""),
		// bx 自己那条 TUN 是**归因的依据本身**:用户看到 "bx (utun11)" 时得能在
		// 这里核对 bx 报的接口确实是 utun11,否则那条归因无从复核。
		"bx tun interface: " + value(local.BXTunInterface, ""),
		"bx protection: " + value(local.BXProtection, ""),
	}
	return evidence
}
