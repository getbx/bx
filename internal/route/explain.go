package route

import "net/netip"

// Source 说明一个分流判定是**哪一层规则**做出的。
//
// 它存在的理由是一次真实排查:配置里五条强制直连规则指向的路早已不通,
// 而 bx 只报得出「direct 26186」—— 判定的**依据**从来没有被记下来,于是
// 「为什么这条连接走了直连」这个问题在事后无法回答,故障被归错因。
type Source int

const (
	// 零值是「没命中任何列表」—— 与本仓库其它判据同一条纪律:
	// 零值必须是最不自信的那个答案,而不是某条具体规则。
	SourceDefault Source = iota
	SourceUserProxy
	SourceUserDirect
	SourceChinaDomain
	SourceUserProxyIP
	SourceUserDirectIP
	SourcePrivate
	SourceChinaCIDR
	// SourceSplitDNS 是 split-DNS 解析出的内网真实 IP —— 它绕过 Router,
	// 由 dialer 直接判直连,故 Router 自己永远不会产出这个值。
	SourceSplitDNS
	// SourceUserEgress 是用户点名交给具名出口的网段(白名单)。
	SourceUserEgress
)

func (s Source) String() string {
	switch s {
	case SourceUserProxy:
		return "user_proxy"
	case SourceUserDirect:
		return "user_direct"
	case SourceChinaDomain:
		return "china_domain"
	case SourceUserProxyIP:
		return "user_proxy_ip"
	case SourceUserDirectIP:
		return "user_direct_ip"
	case SourcePrivate:
		return "private"
	case SourceChinaCIDR:
		return "china_cidr"
	case SourceSplitDNS:
		return "split_dns"
	case SourceUserEgress:
		return "user_egress"
	default:
		return "default"
	}
}

// IsUserRule 判断这个判定是不是**用户自己写的规则**做的。
//
// 归因只对用户规则有意义:内建列表出问题是 bx 的事,而用户规则出问题
// 只有用户改得了 —— 而那正是需要点名到具体哪一行的场合。
func (s Source) IsUserRule() bool {
	switch s {
	case SourceUserProxy, SourceUserDirect, SourceUserProxyIP, SourceUserDirectIP, SourceUserEgress:
		return true
	}
	return false
}

// Reason 是一次判定的依据。Rule 是命中的用户规则**原文**(config 里那一行);
// 内建列表命中时为空 —— 报一个用户搜不到的串不如不报。
type Reason struct {
	Source Source
	Rule   string
}

// Explain 与 Decide 判定完全相同,**并且 Decide 就是它的薄壳**。
//
// 两个函数各写一份判据是本仓库反复栽过的形状:一个改了另一个没改,而两边的
// 测试都绿。这里由构造保证不可能分叉,另有一条测试逐个输入比对以防有人拆开。
func (r *Router) Explain(m Meta) (Decision, Reason) {
	if m.Domain != "" {
		if r.UserProxy != nil {
			if rule, ok := r.UserProxy.MatchRule(m.Domain); ok {
				return Proxy, Reason{Source: SourceUserProxy, Rule: rule}
			}
		}
		if r.UserDirect != nil {
			if rule, ok := r.UserDirect.MatchRule(m.Domain); ok {
				return Direct, Reason{Source: SourceUserDirect, Rule: rule}
			}
		}
		if !r.GlobalProxy && r.ChinaDomain != nil && r.ChinaDomain.Match(m.Domain) {
			return Direct, Reason{Source: SourceChinaDomain}
		}
		// 未命中任何列表:默认走代理。不再用(可能被污染的)国内 DNS 做 geoip,
		// 避免境外域名被误判直连而泄漏真实 IP。裸 IP 连接仍走 ExplainIP 的 geoip。
		return Proxy, Reason{Source: SourceDefault}
	}
	if m.IP.IsValid() {
		return r.ExplainIP(m.IP)
	}
	return Proxy, Reason{Source: SourceDefault} // 信息不足时保守走代理
}

// ExplainIP 与 DecideIP 判定完全相同,DecideIP 是它的薄壳。
func (r *Router) ExplainIP(ip netip.Addr) (Decision, Reason) {
	// **具名出口排在最前,压过「私网恒直连」。**
	//
	// 这是刻意的,也是这个功能存在的全部理由:用户要访问的 10.84.3.239 落在
	// 10/8 里,而那条不变量会把它送去本地网卡 —— 那台机器不在这边的局域网。
	// 只有用户**逐条写出来**的网段才走到这里(白名单),docker / LAN / SSH
	// 一个都不受影响。
	if r.UserEgress != nil {
		if name, ok := r.UserEgress.Lookup(ip); ok {
			return Via, Reason{Source: SourceUserEgress, Rule: name}
		}
	}
	if r.UserProxyIP != nil && r.UserProxyIP.Contains(ip) {
		return Proxy, Reason{Source: SourceUserProxyIP}
	}
	if r.UserDirectIP != nil && r.UserDirectIP.Contains(ip) {
		return Direct, Reason{Source: SourceUserDirectIP}
	}
	// 私网/docker/link-local:用户未显式覆盖时一律直连(不受 global 影响)。
	if r.PrivateDirect != nil && r.PrivateDirect.Contains(ip) {
		return Direct, Reason{Source: SourcePrivate}
	}
	if !r.GlobalProxy && r.ChinaCIDR != nil && r.ChinaCIDR.Contains(ip) {
		return Direct, Reason{Source: SourceChinaCIDR}
	}
	return Proxy, Reason{Source: SourceDefault}
}
