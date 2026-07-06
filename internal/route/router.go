package route

import "net/netip"

// DefaultPrivateCIDRs 是任何模式下都内建直连的非公网段:RFC1918 私网、
// docker 默认地址池(172.16.0.0/12)、link-local、CGNAT、loopback。
// 透明代理把这些段隧道给远端服务器没有意义(远端到不了你的内网/容器),
// 故默认直连,走宿主机原路由表(table main)交给 docker0/lo/网关投递。
// 注意:不含 fakeip 段(198.18.0.0/15)——那要被 DNS 处理器拦截反查。
var DefaultPrivateCIDRs = []string{
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
	"169.254.0.0/16",
	"100.64.0.0/10",
	"127.0.0.0/8",
}

// DefaultPrivateV6CIDRs 是任何模式下都内建直连的 v6 非全局段,对应 v4 的 DefaultPrivateCIDRs:
// loopback、link-local、ULA(私网)、multicast(mDNS/NDP/RA)。bx 对全局 v6 fail-closed 阻断
// (unreachable 默认路由),这些段必须 carve-out 走主表直连,否则打断局域网 / 邻居发现。
var DefaultPrivateV6CIDRs = []string{
	"::1/128",   // loopback
	"fe80::/10", // link-local
	"fc00::/7",  // ULA(对应 v4 私网)
	"ff00::/8",  // multicast(mDNS / NDP / RA)
}

// Router 是纯逻辑分流脑。所有字段由上层从配置构建后注入。
type Router struct {
	UserDirect    *DomainSet // 用户强制直连域名
	UserProxy     *DomainSet // 用户强制代理域名
	UserDirectIP  *CIDRSet   // 用户强制直连网段
	UserProxyIP   *CIDRSet   // 用户强制代理网段(可选)
	PrivateDirect *CIDRSet   // 内建私网/docker 直连段(DefaultPrivateCIDRs)
	ChinaDomain   *DomainSet // 国内域名列表
	ChinaCIDR     *CIDRSet   // 国内 IP 段(geoip-cn)
	GlobalProxy   bool       // 全局模式:跳过 china 判定,除用户 direct 规则外一律走代理
}

// Decide 按优先级判定。返回 NeedResolve 表示有域名但未命中,
// 上层应解析出 IP 后调用 DecideIP。
func (r *Router) Decide(m Meta) Decision {
	if m.Domain != "" {
		switch {
		case r.UserProxy != nil && r.UserProxy.Match(m.Domain):
			return Proxy
		case r.UserDirect != nil && r.UserDirect.Match(m.Domain):
			return Direct
		case !r.GlobalProxy && r.ChinaDomain != nil && r.ChinaDomain.Match(m.Domain):
			return Direct
		default:
			// 未命中任何列表:默认走代理。
			// 不再用(可能被污染的)国内 DNS 做 geoip,避免境外域名被误判
			// 直连而泄漏真实 IP。裸 IP 连接仍走 DecideIP 的 geoip。
			return Proxy
		}
	}
	if m.IP.IsValid() {
		return r.DecideIP(m.IP)
	}
	return Proxy // 信息不足时保守走代理
}

// DecideIP 仅按 IP 判定:用户 proxy 网段 > 用户 direct 网段 > 私网 > geoip-cn > 默认代理。
func (r *Router) DecideIP(ip netip.Addr) Decision {
	if r.UserProxyIP != nil && r.UserProxyIP.Contains(ip) {
		return Proxy
	}
	if r.UserDirectIP != nil && r.UserDirectIP.Contains(ip) {
		return Direct
	}
	// 私网/docker/link-local:用户未显式覆盖时一律直连(不受 global 影响)。
	if r.PrivateDirect != nil && r.PrivateDirect.Contains(ip) {
		return Direct
	}
	if !r.GlobalProxy && r.ChinaCIDR != nil && r.ChinaCIDR.Contains(ip) {
		return Direct
	}
	return Proxy
}
