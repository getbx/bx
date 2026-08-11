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
	SRFLX   []string `json:"srflx"`
	STUNErr string   `json:"stun_err"`
}

// Empty 判断这份上报里有没有任何**观测结果**。三个 Err 刻意不参与判断:一份
// 三项全失败的上报里,观测结果依然是零。
func (b BrowserReport) Empty() bool {
	return b.ExitV4 == "" && b.ExitV6 == "" && len(b.SRFLX) == 0
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
	return b.Empty() && b.ExitV4Err == "" && b.ExitV6Err == "" && b.STUNErr == ""
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
}
