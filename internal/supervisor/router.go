// Package supervisor 顶层编排:把配置、隧道、TUN 引擎、分流脑接成可运行的 bx。
package supervisor

import (
	"net/netip"
	"strings"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/route"
)

// BuildRouter 从配置规则 + china 列表构建分流脑。
// 规则里的条目按"是不是 CIDR/IP"分流到 IP 集或域名集。
func BuildRouter(cfg *config.Config, chinaDomain, chinaCIDR []string) (*route.Router, error) {
	var directDoms, proxyDoms, directCIDRs, proxyCIDRs []string
	for _, rule := range cfg.Rules {
		for _, e := range rule.Direct {
			if cidr, ok := asCIDR(e); ok {
				directCIDRs = append(directCIDRs, cidr)
			} else {
				directDoms = append(directDoms, e)
			}
		}
		for _, e := range rule.Proxy {
			if cidr, ok := asCIDR(e); ok {
				proxyCIDRs = append(proxyCIDRs, cidr)
			} else {
				proxyDoms = append(proxyDoms, e)
			}
		}
	}

	directIP, err := route.NewCIDRSet(directCIDRs)
	if err != nil {
		return nil, err
	}
	proxyIP, err := route.NewCIDRSet(proxyCIDRs)
	if err != nil {
		return nil, err
	}
	cnIP, err := route.NewCIDRSet(chinaCIDR)
	if err != nil {
		return nil, err
	}

	return &route.Router{
		UserDirect:   route.NewDomainSet(directDoms),
		UserProxy:    route.NewDomainSet(proxyDoms),
		UserDirectIP: directIP,
		UserProxyIP:  proxyIP,
		ChinaDomain:  route.NewDomainSet(chinaDomain),
		ChinaCIDR:    cnIP,
	}, nil
}

// asCIDR 把条目识别为网段:已是 CIDR 原样返回;裸 IP 补成 /32 或 /128;
// 否则(域名模式)返回 ok=false。
func asCIDR(s string) (string, bool) {
	if strings.Contains(s, "/") {
		if _, err := netip.ParsePrefix(s); err == nil {
			return s, true
		}
		return "", false
	}
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return "", false
	}
	return netip.PrefixFrom(addr, addr.BitLen()).String(), true
}
