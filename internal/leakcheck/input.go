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

// Empty 判断这份上报是不是「什么都没有」。页面没跑成、用户直接关掉标签页、
// 服务硬超时,送进来的都是零值。
func (b BrowserReport) Empty() bool {
	return b.ExitV4 == "" && b.ExitV6 == "" && len(b.SRFLX) == 0
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
