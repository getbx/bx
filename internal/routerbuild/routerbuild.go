// Package routerbuild 把配置规则与 china 列表拼成分流脑(route.Router)。
//
// 它原本住在 supervisor(BuildRouter),而 supervisor 起进程、改路由 —— 手机端要按**同一份**
// 判据回答「这个目的地走哪」,就不能去依赖它。判据只在这里一份;supervisor.BuildRouter 是
// 薄壳。本包是纯判据(purity_test.go),会被 verify 为 ios/android 交叉编译。
package routerbuild

import (
	"strings"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/policy"
	"github.com/getbx/bx/internal/route"
)

// Build 从配置规则 + china 列表构建分流脑(GlobalProxy 由调用方按 cfg.Global 与运行期开关设)。
// 规则里的条目按"是不是 CIDR/IP"分流到 IP 集或域名集。
func Build(cfg *config.Config, chinaDomain, chinaCIDR []string) (*route.Router, error) {
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
	privateIP, err := route.NewCIDRSet(route.DefaultPrivateCIDRs)
	if err != nil {
		return nil, err
	}
	// 具名出口的白名单。**只收 rules 里带 via 的那几条**,没有任何推断。
	var egressPairs [][2]string
	for _, rule := range cfg.Rules {
		via := strings.TrimSpace(rule.Via)
		if via == "" {
			continue
		}
		for _, c := range rule.CIDR {
			egressPairs = append(egressPairs, [2]string{via, strings.TrimSpace(c)})
		}
	}
	egressSet, err := route.NewEgressSet(egressPairs)
	if err != nil {
		return nil, err
	}

	return &route.Router{
		UserDirect:    route.NewDomainSet(directDoms),
		UserProxy:     route.NewDomainSet(proxyDoms),
		UserDirectIP:  directIP,
		UserProxyIP:   proxyIP,
		PrivateDirect: privateIP,
		UserEgress:    egressSet,
		ChinaDomain:   route.NewDomainSet(chinaDomain),
		ChinaCIDR:     cnIP,
	}, nil
}

// asCIDR 把条目识别为网段:已是 CIDR 原样返回;裸 IP 补成 /32 或 /128;
// 否则(域名模式)返回 ok=false。**判定住在 internal/policy,这里是薄壳。**
func asCIDR(s string) (string, bool) {
	p, ok := policy.RuleCIDR(s)
	if !ok {
		return "", false
	}
	return p.String(), true
}
