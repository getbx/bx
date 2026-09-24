package leakserve

import (
	"context"
	"errors"
	"fmt"
	"net"

	"github.com/getbx/bx/internal/supervisor"
)

// 「绕过隧道」那条路(spec §5 路径 B,known-gaps B3,2026-09-23)。
//
// **只在 `bx leakcheck --compare-direct` 时跑**(所有者定的 opt-in):它从物理网卡直接
// 发请求,Anthropic / OpenAI / Google 会看到用户的**真实 IP**。默认那一轮不跑它,
// DefaultProbeBypass 仍是 false、LiveReachDeps 仍不供货。

// bypassResolverAddr 是直连那条路用的解析器,经**同一块物理网卡**去问。
//
// **不许用系统解析器**:bx 开着时系统 DNS 是 bx 自己(fake-IP),答回来的是
// 198.18/15 的假地址,从物理网卡发出去必然石沉大海 —— 直连会被判成「不通」,
// 而真正的原因是我们问错了人。1.1.1.1 在墙内可能被投毒,那时 TLS 会失败、这条路
// 判「不通」,而那恰恰是一个直连用户真实会遇到的结果。
const bypassResolverAddr = "1.1.1.1:53"

// errBypassResolve:名字在物理网卡上解析不出来。归「没测成」,不归「不通」——
// 我们分不出是解析器被墙、这块网卡没网,还是对方真的不存在。
var errBypassResolve = errors.New("the name could not be resolved over the physical network interface")

// bypassDial 把「先解析、再拨号」两步都关在注入的原语里:dial 是绑了物理网卡的
// 拨号,lookup 是经同一块网卡问的解析器。只取 IPv4(bx 对 v6 是 fail-closed 阻断的,
// 用户的直连体验本来就是 v4)。
func bypassDial(dial DialFunc, lookup func(ctx context.Context, host string) ([]net.IP, error)) DialFunc {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		if net.ParseIP(host) != nil {
			return dial(ctx, "tcp4", addr)
		}
		ips, err := lookup(ctx, host)
		if err != nil || len(ips) == 0 {
			return nil, fmt.Errorf("%w (%s): %v", errBypassResolve, host, err)
		}
		var last error
		for _, ip := range ips {
			if ip.To4() == nil {
				continue
			}
			c, err := dial(ctx, "tcp4", net.JoinHostPort(ip.String(), port))
			if err == nil {
				return c, nil
			}
			last = err
		}
		if last == nil {
			return nil, fmt.Errorf("%w (%s): no IPv4 address", errBypassResolve, host)
		}
		return nil, last
	}
}

// withBypassDial 往一份 deps 上加直连那条路。**整轮探测被关掉时不加**(--no-reach
// 的人没要求 bx 去连任何人;CLI 那一侧另外把两个 flag 同时出现当成错误报出来)。
func withBypassDial(deps ReachDeps, bypass DialFunc) ReachDeps {
	if !deps.WillProbe() {
		return deps
	}
	deps.BypassDial = bypass
	return deps
}

// WithBypass 给 deps 接上生产环境的直连拨号器。拿不到物理网卡(非 macOS、默认路由
// 问不出来)就**报错**,不退回一个不绑网卡的拨号器 —— 用户显式要了对照,悄悄给他
// 一条和当前路径一模一样的「直连」,比告诉他做不到更糟。
func WithBypass(ctx context.Context, deps ReachDeps) (ReachDeps, error) {
	d, err := supervisor.PhysicalInterfaceDialer(ctx)
	if err != nil {
		return deps, err
	}
	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return d.DialContext(ctx, "udp4", bypassResolverAddr)
		},
	}
	lookup := func(ctx context.Context, host string) ([]net.IP, error) {
		return resolver.LookupIP(ctx, "ip4", host)
	}
	return withBypassDial(deps, bypassDial(d.DialContext, lookup)), nil
}
