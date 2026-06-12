package dialer

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"sync/atomic"

	"github.com/getbx/bx/internal/fakeip"
	"github.com/getbx/bx/internal/route"
	"github.com/getbx/bx/internal/splitdns"
)

// ErrBlocked 表示连接被 kill-switch 或 Block 决策拦截。
var ErrBlocked = errors.New("blocked by killswitch")

// ContextDialer 是带 context 的拨号器(net.Dialer 与 socks5 dialer 都满足)。
type ContextDialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

// Resolver 把域名解析为 IP(直连判定用国内 DNS)。
type Resolver interface {
	Resolve(ctx context.Context, domain string) (netip.Addr, error)
}

// DecisionCounter 按分流决策计数(由 stats.Counters 实现)。
type DecisionCounter interface {
	Proxy()
	Direct()
	Blocked()
}

// Dialer 把 Router 决策落到实际拨号。
type Dialer struct {
	router     atomic.Pointer[route.Router]
	Fake       *fakeip.Pool // 可空:无 fake-IP 时按 IP 直判
	Resolver   Resolver
	Proxy      ContextDialer // 经 brook socks5
	Direct     ContextDialer // 直连
	Healthy    func() bool   // 隧道是否健康(kill-switch 用),可空
	Killswitch bool
	Stats      DecisionCounter // 可空:决策计数
	// SplitDirect 可空:split-DNS 解析出的内网真实 IP 集,命中即强制直连(绕 Router)。
	SplitDirect *splitdns.Set
}

// SetRouter 原子替换当前分流脑(用于列表刷新后的热重载)。
func (d *Dialer) SetRouter(r *route.Router) { d.router.Store(r) }

func network(udp bool) string {
	if udp {
		return "udp"
	}
	return "tcp"
}

// Dial 处理一条来自 TUN 的连接,返回到出口的 net.Conn。
func (d *Dialer) Dial(ctx context.Context, m route.Meta) (net.Conn, error) {
	rt := d.router.Load()
	// 1) fake IP 反查域名
	if m.Domain == "" && d.Fake != nil {
		if dom, ok := d.Fake.Domain(m.IP); ok {
			m.Domain = dom
		}
	}

	var dec route.Decision
	if m.Domain == "" && d.SplitDirect != nil && d.SplitDirect.Contains(m.IP) {
		dec = route.Direct // split 解析出的内网真实 IP:强制直连,跳过 Router
	} else {
		dec = rt.Decide(m)
	}

	// 2) 未命中域名:用国内 DNS 解析后按 IP 二次判定
	var resolved netip.Addr
	if dec == route.NeedResolve {
		ip, err := d.Resolver.Resolve(ctx, m.Domain)
		if err != nil {
			dec = route.Proxy // 解析失败保守走代理
		} else {
			resolved = ip
			dec = rt.DecideIP(ip)
		}
	}

	port := strconv.Itoa(int(m.Port))
	switch dec {
	case route.Direct:
		if d.Stats != nil {
			d.Stats.Direct()
		}
		if m.Domain != "" {
			ip := resolved
			if !ip.IsValid() {
				r, err := d.Resolver.Resolve(ctx, m.Domain)
				if err != nil {
					return nil, err
				}
				ip = r
			}
			return d.Direct.DialContext(ctx, network(m.UDP), net.JoinHostPort(ip.String(), port))
		}
		return d.Direct.DialContext(ctx, network(m.UDP), net.JoinHostPort(m.IP.String(), port))

	case route.Proxy:
		if d.Killswitch && d.Healthy != nil && !d.Healthy() {
			if d.Stats != nil {
				d.Stats.Blocked()
			}
			return nil, ErrBlocked
		}
		if d.Stats != nil {
			d.Stats.Proxy()
		}
		host := m.Domain
		if host == "" {
			host = m.IP.String()
		}
		return d.Proxy.DialContext(ctx, network(m.UDP), net.JoinHostPort(host, port))

	default: // Block
		if d.Stats != nil {
			d.Stats.Blocked()
		}
		return nil, ErrBlocked
	}
}
