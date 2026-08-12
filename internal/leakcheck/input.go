package leakcheck

import "github.com/getbx/bx/internal/tristate"

// BrowserReport 是页面 POST 回来的**原始**观测。全部是字符串:页面不判断、
// 不归一化、不比较,它只是把看到的东西原样送回来。
//
// 每一项都配一个 Err:失败与「没跑」在下游必须分得开,而两者都绝不能变成 ok。
type BrowserReport struct {
	UserAgent string `json:"user_agent"`
	// ExitV4 是 v4-only 回声端看到的源地址。
	ExitV4    string `json:"exit_v4"`
	ExitV4Err string `json:"exit_v4_err"`
	// ExitV6 是 v6-only 回声端看到的源地址。
	ExitV6    string `json:"exit_v6"`
	ExitV6Err string `json:"exit_v6_err"`
	// SRFLX 是 STUN 问出来的 server-reflexive 地址。**只要 srflx。**
	// host candidate 早被浏览器 mDNS 混淆了(<uuid>.local),按那个年代的做法
	// 实现会得到一条恒绿的检查,而恒绿比没有更糟。
	SRFLX []string `json:"srflx"`
	// HostCandidates 是同一次 ICE gathering 里的 `typ host` 地址。2019 年之后的
	// 浏览器把它们换成随机的 `<uuid>.local`,**而「有没有被换掉」本身就是一条
	// 关于这台机器的事实**:没换掉的那台,局域网结构对每个网站可见。
	HostCandidates []string `json:"host_candidates"`
	// ExitCountry / TraceExitV4 来自 TraceURL:出口国家(ISO 3166-1 alpha-2)与
	// 它看到的出口地址。TraceErr 记这一跳的失败 —— 与其余各项同一条纪律:
	// **失败与「没跑」在下游必须分得开,而两者都不能变成 ok。**
	ExitCountry string `json:"exit_country"`
	TraceExitV4 string `json:"trace_exit_v4"`
	TraceErr    string `json:"trace_err"`
	// Timezone 是浏览器自报的 IANA 时区名(`Asia/Shanghai`)。
	Timezone string `json:"timezone"`
	// CanvasA / CanvasB 是**同一次会话里连画两遍**同一张 canvas 得到的指纹。
	// 两遍相同 = 浏览器没做任何防护;不同 = 它在加噪(Brave / Tor 那一类)。
	// **问的是「有没有在防」,不是「指纹是什么」** —— 后者要算唯一性,而没有
	// 语料库就没有分母,编一个百分比比不报更糟。
	CanvasA string `json:"canvas_a"`
	CanvasB string `json:"canvas_b"`
	// 下面这些没有正确答案,只是网站读得到。它们进 surface 段,永远是 Info。
	Languages           []string `json:"languages,omitempty"`
	Screen              string   `json:"screen,omitempty"`
	HardwareConcurrency string   `json:"hardware_concurrency,omitempty"`
	DeviceMemory        string   `json:"device_memory,omitempty"`
	WebGLRenderer       string   `json:"webgl_renderer,omitempty"`
	STUNErr             string   `json:"stun_err"`
}

// Empty 判断这份上报里有没有任何**观测结果**。三个 Err 刻意不参与判断:一份
// 三项全失败的上报里,观测结果依然是零。
func (b BrowserReport) Empty() bool {
	return b.ExitV4 == "" && b.ExitV6 == "" && len(b.SRFLX) == 0 &&
		len(b.HostCandidates) == 0 && b.ExitCountry == "" && b.Timezone == ""
}

// Silent 判断浏览器那一半**从来没到过**:既没有任何观测结果(Empty),也没有
// 任何一条失败原因。
//
// **与 Empty 的区别是承重的,不是修辞**,依赖浏览器的两条规则据此分岔:
//
//   - 页面跑过、三项全失败 —— 也是 Empty,但那三个 Err 就是它跑过的证据。
//     IPv6 那条据此给出的「确知本机没有 v6 默认路由 + 浏览器确实试过 ⇒ 没有
//     可漏的 v6 通路」是一句**诚实的 ok**,不许一并压掉。
//   - 页面从没跑成 —— 用户开了标签页却没点那个按钮就关掉、浏览器压根没打开、
//     POST 没打进来、硬超时后 Wait 合成的那份零值上报。什么都没观测,也说不出
//     为什么。
//
// 后者绝不许支撑任何一条依赖浏览器的结论:「没看到泄漏」不是「没有泄漏」,
// 而「根本没去看」连「没看到」都不是。页面在**每一条**采集路径上都会留下 value
// 或 err(空响应体也记成 err),所以真跑过的上报到不了这里 —— 万一到了,
// 保守成 not checked 也是安全的那一边。
func (b BrowserReport) Silent() bool {
	return b.Empty() && b.ExitV4Err == "" && b.ExitV6Err == "" && b.STUNErr == "" && b.TraceErr == ""
}

// InterfaceRef 是一个网络接口,以及它翻成人话之后的名字。
//
// Display 为空表示**翻不出来**,不是「没有接口」——两者在渲染时必须分开说
// (设计诚实性规则:「无法识别」是一个合法答案,猜不是)。
type InterfaceRef struct {
	Name    string `json:"name,omitempty"`
	Display string `json:"display,omitempty"`
	Err     string `json:"err,omitempty"`
}

// Known 表示这一项确实观测到了一个接口。
func (r InterfaceRef) Known() bool { return r.Name != "" && r.Err == "" }

// RouteEntry 是路由表里的一行,**原样**记录,不归一化不判断。
type RouteEntry struct {
	Destination string `json:"destination"`
	Interface   string `json:"interface"`
	Flags       string `json:"flags"`
}

// LocalFacts 是只有本机能看到的那一半。全部只读采集,采不到就留空并记 Err。
type LocalFacts struct {
	// DefaultRouteV4/V6:发往公网时内核把包交给谁。
	DefaultRouteV4 InterfaceRef `json:"default_route_v4"`
	DefaultRouteV6 InterfaceRef `json:"default_route_v6"`
	// IPv6DefaultPresent 是**谓词**,所以这里复用 tristate.Tristate:零值 Unknown
	// 就是「没问出来」,与「问了,确实没有 v6 默认路由」必须分开 —— 后者能支撑
	// 一句诚实的 ok,前者不能。
	IPv6DefaultPresent tristate.Tristate `json:"ipv6_default_present"`
	// DNSServers 是系统当前的解析器。
	DNSServers []string `json:"dns_servers,omitempty"`
	// DNSServerEgress 是每个解析器地址的出口接口(route 查出来的)。
	// 少一个键 = 那个解析器的去向没问出来。
	DNSServerEgress map[string]string `json:"dns_server_egress,omitempty"`
	DNSErr          string            `json:"dns_err,omitempty"`
	// BXTunInterface / BXProtection 来自 Guardian 的 /v1/status(普通用户可读)。
	// 空串表示 Guardian 没答上话,不表示 bx 没在跑。
	BXTunInterface string `json:"bx_tun_interface,omitempty"`
	BXProtection   string `json:"bx_protection,omitempty"`
	// BXUDPMode / BXUDPTransport 是 bx **此刻正在用**的 UDP 分流配置(经 Guardian
	// 转发的 Core 运行时值,不是盘上的 config)。
	//
	// 它们只用来**解释**一个已经观测到的不一致,绝不用来产生结论:WebRTC 那条
	// 靠它们把「bx 自己把 UDP 送去了另一条隧道」与「WebRTC 真的漏了」分开,
	// 而两者在 srflx 那个地址上长得一模一样。
	BXUDPMode      string `json:"bx_udp_mode,omitempty"`
	BXUDPTransport string `json:"bx_udp_transport,omitempty"`
	// Routes 是本机 IPv4 路由表的原始条目。**只采,不判** —— 判断在 judgeRouteEscape。
	// RoutesErr 非空表示没读出来,与「读出来了、是空的」必须分开。
	Routes    []RouteEntry `json:"routes,omitempty"`
	RoutesErr string       `json:"routes_err,omitempty"`
}
