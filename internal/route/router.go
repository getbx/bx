package route

import "net/netip"

// Router 是纯逻辑分流脑。所有字段由上层从配置构建后注入。
type Router struct {
	UserDirect   *DomainSet // 用户强制直连域名
	UserProxy    *DomainSet // 用户强制代理域名
	UserDirectIP *CIDRSet   // 用户强制直连网段
	UserProxyIP  *CIDRSet   // 用户强制代理网段(可选)
	ChinaDomain  *DomainSet // 国内域名列表
	ChinaCIDR    *CIDRSet   // 国内 IP 段(geoip-cn)
	GlobalProxy  bool       // 全局模式:跳过 china 判定,除用户 direct 规则外一律走代理
}

// Decide 按优先级判定。返回 NeedResolve 表示有域名但未命中,
// 上层应解析出 IP 后调用 DecideIP。
func (r *Router) Decide(m Meta) Decision {
	if m.Domain != "" {
		switch {
		case r.UserDirect != nil && r.UserDirect.Match(m.Domain):
			return Direct
		case r.UserProxy != nil && r.UserProxy.Match(m.Domain):
			return Proxy
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

// DecideIP 仅按 IP 判定:用户网段 > geoip-cn > 默认代理。
func (r *Router) DecideIP(ip netip.Addr) Decision {
	if r.UserDirectIP != nil && r.UserDirectIP.Contains(ip) {
		return Direct
	}
	if r.UserProxyIP != nil && r.UserProxyIP.Contains(ip) {
		return Proxy
	}
	if !r.GlobalProxy && r.ChinaCIDR != nil && r.ChinaCIDR.Contains(ip) {
		return Direct
	}
	return Proxy
}
