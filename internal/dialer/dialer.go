package dialer

import (
	"context"
	"errors"
	"log"
	"net"
	"net/netip"
	"os"
	"strconv"
	"sync/atomic"

	"github.com/getbx/bx/internal/fakeip"
	"github.com/getbx/bx/internal/route"
	"github.com/getbx/bx/internal/splitdns"
)

// ErrBlocked 表示连接被 kill-switch 或 Block 决策拦截。
var ErrBlocked = errors.New("blocked by killswitch")

var debug = os.Getenv("BX_DEBUG") != ""

func debugf(format string, args ...any) {
	if debug {
		log.Printf(format, args...)
	}
}

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
	UDPBlocked()
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
	UDPMode    string          // block(默认), direct-realtime, proxy(预留)
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
	return d.DialWithInitial(ctx, m, nil)
}

// DialWithInitial 可用 TCP 首包中的 TLS SNI / HTTP Host 为未知 fake-IP 恢复域名。
func (d *Dialer) DialWithInitial(ctx context.Context, m route.Meta, initial []byte) (net.Conn, error) {
	rt := d.router.Load()
	// 1) fake IP 反查域名
	if m.Domain == "" && d.Fake != nil {
		if dom, ok := d.Fake.Domain(m.IP); ok {
			m.Domain = dom
		} else if sniffed := sniffDomain(initial); sniffed != "" {
			m.Domain = sniffed
			debugf("domain sniffed: ip=%s domain=%q port=%d udp=%v", m.IP, m.Domain, m.Port, m.UDP)
		} else if m.IP.Is4() {
			debugf("fake-ip miss: ip=%s port=%d udp=%v", m.IP, m.Port, m.UDP)
		}
	}

	if m.UDP {
		if d.UDPMode == "direct-realtime" {
			if d.Stats != nil {
				d.Stats.Direct()
			}
			ip := m.IP
			if m.Domain != "" {
				resolved, err := d.Resolver.Resolve(ctx, m.Domain)
				if err != nil {
					return nil, err
				}
				ip = resolved
			}
			target := net.JoinHostPort(ip.String(), strconv.Itoa(int(m.Port)))
			debugf("udp direct-realtime: ip=%s target=%s", m.IP, target)
			return d.Direct.DialContext(ctx, "udp", target)
		}
		if d.UDPMode == "proxy" {
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
			target := net.JoinHostPort(host, strconv.Itoa(int(m.Port)))
			debugf("udp proxy: ip=%s domain=%q target=%s", m.IP, m.Domain, target)
			return d.Proxy.DialContext(ctx, "udp", target)
		}
		if d.Stats != nil {
			d.Stats.Blocked()
			d.Stats.UDPBlocked()
		}
		debugf("udp blocked: ip=%s domain=%q port=%d", m.IP, m.Domain, m.Port)
		return nil, ErrBlocked
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
		var target string
		if m.Domain != "" {
			ip := resolved
			if !ip.IsValid() {
				r, err := d.Resolver.Resolve(ctx, m.Domain)
				if err != nil {
					return nil, err
				}
				ip = r
			}
			target = net.JoinHostPort(ip.String(), port)
		} else {
			target = net.JoinHostPort(m.IP.String(), port)
		}
		debugf("dial direct: domain=%q ip=%s target=%s udp=%v", m.Domain, m.IP, target, m.UDP)
		conn, err := d.Direct.DialContext(ctx, network(m.UDP), target)
		if err != nil {
			debugf("dial direct failed: target=%s err=%v", target, err)
		}
		return conn, err

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
		target := net.JoinHostPort(host, port)
		debugf("dial proxy: domain=%q ip=%s target=%s udp=%v", m.Domain, m.IP, target, m.UDP)
		conn, err := d.Proxy.DialContext(ctx, network(m.UDP), target)
		if err != nil {
			debugf("dial proxy failed: target=%s err=%v", target, err)
		}
		return conn, err

	default: // Block
		if d.Stats != nil {
			d.Stats.Blocked()
		}
		return nil, ErrBlocked
	}
}
