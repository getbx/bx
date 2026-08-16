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

// InterfaceKind 是内核对一个接口的说法。零值是 InterfaceUnknown —— 与本包其余
// 各处同一条纪律:**没问出来是零值,不是某个具体答案**。
type InterfaceKind uint8

const (
	InterfaceUnknown InterfaceKind = iota
	// InterfaceTunnel:点对点设备。macOS 的 utun、Linux 的 IFF_TUN 都是这个
	// (2026-08-12 两平台实测)。
	InterfaceTunnel
	// InterfacePhysical:广播段。以太网、Wi-Fi,**以及 Linux 的 IFF_TAP** ——
	// TAP 是二层设备,内核眼里它就是一段以太网,标志上与物理网卡无法区分。
	// 这正是名字表不能退休的原因。
	InterfacePhysical
	InterfaceLoopback
)

// RouteEntry 是路由表里的一行,**原样**记录,不归一化不判断。
type RouteEntry struct {
	Destination string `json:"destination"`
	Interface   string `json:"interface"`
	// Flags 是平台原样的标志串,**只作证据出示给人看**,判据不解析它。
	Flags string `json:"flags,omitempty"`
	// Blocking:这条路由把流量**扔掉**而不是送走(darwin 的 R/B 标志、
	// Linux 的 unreachable/blackhole/prohibit)。bx 自己的 fail-closed 屏障就是
	// 这种,把它当成「有人在抢」是这套检查能犯的最蠢的错。
	Blocking bool `json:"blocking,omitempty"`
	// Scoped:接口作用域路由(darwin 的 RTF_IFSCOPE),只对已绑定到该接口的流量
	// 生效,不参与一般流量的竞争。
	//
	// **Blocking / Scoped 由采集层判定,不由判据层。** 此前判据直接匹配 darwin 的
	// flag 字母,而 Linux 根本没有那些字母 —— 在 Linux 上合成一串假 flags 去迁就
	// 它,就是把平台差异伪装成一个共同事实。谁知道那个平台,谁负责翻译。
	Scoped bool `json:"scoped,omitempty"`
	// OnLink:这段地址**直接挂在这个接口上**(没有下一跳网关)。
	//
	// 它不是「有人把发往这里的包交给了别处」,而是「这段就在这条线上」——
	// 而注入攻击(DHCP option 121 / RA)要的恰恰是前者。不区分的话,一台 LAN 上
	// 带公网网段的 VPS/路由器会被恒报成 CVE-2024-3661,而那正是 bx 明确支持的部署形态。
	OnLink bool `json:"on_link,omitempty"`
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

	// 系统的时区与语言。**本机读得到,不需要浏览器。**
	//
	// **收益范围要说准(2026-08-16 当场纠正过一次)**:它买到的是「页面那条在
	// 浏览器不报这两项时仍然判得了」(有的浏览器裁掉 Intl,有的不发语言)。
	// 它**不**帮到 `bx leak-check` —— 那条命令是另一套输出(checks 而不是
	// findings),根本不走 Judge。当时提交信息里写反了,在这里记一笔,免得
	// 下一个人照着那句去找它。
	//
	// **浏览器的值优先。** 网站看到的是浏览器报的那个,系统值只是它的来源;
	// 两者可以不同(浏览器语言是单独设的)。所以系统值是**回落**,而且结论里
	// 要说清这次用的是哪一个 —— 拿系统值去断言「网站看到的是这个」是在冒认。
	SystemTimezone  string   `json:"system_timezone,omitempty"`
	SystemLanguages []string `json:"system_languages,omitempty"`
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
	// OverlayTenants 是此刻在跑的 overlay 网络名(tailscale / zerotier …)。
	//
	// **它只用来解释一个已经观测到的事实,绝不用来产生结论。** 用户对 VPN 与 overlay
	// 的心理模型不同:两个 VPN 一起跑他预期冲突,bx 加 Tailscale 他预期「本来就该
	// 共存」。所以共存正常时 bx 不该提它;只有共存被打破时(默认路由被别人拿走),
	// 才用它去点名最可能的嫌疑。
	OverlayTenants []string `json:"overlay_tenants,omitempty"`
	// InterfaceKinds 是内核对每个接口的说法,由采集层从 net.Flags 翻译而来。
	//
	// **它比名字硬。** 名字判据是按 macOS 命名建的,连 bx 自己在 Linux/Windows 上的
	// bx0 都不认识 —— 真 Linux 容器里当场看到的后果是「一台受保护的机器上,路由逃逸
	// 那条报『没有隧道在管』」。而 pointtopoint 这个标志跨平台一致(实测:macOS 的
	// utun、Linux 的 IFF_TUN 都是它)。
	//
	// 翻译在采集层做,不在这里:本包的纯度守卫禁止 import net。
	InterfaceKinds map[string]InterfaceKind `json:"interface_kinds,omitempty"`
	// InterfaceAddrs 是每个接口自己持有的地址。
	//
	// **它是 on-link 豁免能成立的前提。** 「这段挂在这条线上」的真正判据不是
	// 「没有下一跳」——RFC 3442 的 option 121 允许 router 为 0.0.0.0,装出来的正是
	// 一条没有下一跳的 `64.0.0.0/2 dev eth0 scope link`;RA 注入的 v6 前缀同样没有。
	// 真正的直连子网,本机在里面**有地址**。
	InterfaceAddrs map[string][]string `json:"interface_addrs,omitempty"`
	// Routes 是本机 IPv4 路由表的原始条目。**只采,不判** —— 判断在 judgeRouteEscape。
	// RoutesErr 非空表示没读出来,与「读出来了、是空的」必须分开。
	Routes    []RouteEntry `json:"routes,omitempty"`
	RoutesErr string       `json:"routes_err,omitempty"`
}
