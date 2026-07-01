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
	router       atomic.Pointer[route.Router]
	transport    atomic.Pointer[Transport] // 取代裸 Proxy/Healthy:运行期可原子换隧道
	udpTransport atomic.Pointer[Transport] // 可空:UDP 专用传输(如 hysteria);nil=UDP 用主传输。按类分流的速度档
	Fake         *fakeip.Pool              // 可空:无 fake-IP 时按 IP 直判
	Resolver     Resolver
	Direct       ContextDialer // 直连
	Killswitch   bool
	Stats        DecisionCounter // 可空:决策计数
	UDPMode      string          // proxy(默认,走隧道), direct-realtime(直连真实 IP), block
	// SplitDirect 可空:split-DNS 解析出的内网真实 IP 集,命中即强制直连(绕 Router)。
	SplitDirect *splitdns.Set

	leakWarned atomic.Bool // direct-realtime 真实 IP 外泄告警只打一次
}

// Transport 是一次可原子替换的传输(socks 代理 + 健康判定),供运行期换隧道。
type Transport struct {
	Proxy   ContextDialer // 经隧道 socks5
	Healthy func() bool   // 隧道健康(kill-switch 用);可空
}

// SetTransport 原子替换当前传输(proxy + healthy 一并换,绝不半换)。
func (d *Dialer) SetTransport(t *Transport) { d.transport.Store(t) }

// SetUDPTransport 设 UDP 专用传输(按类分流的速度档,如 hysteria2);nil 则 UDP 走主传输。
// 不变量:该传输不健康时 UDP proxy 仍 fail-closed Block,绝不回落直连/主传输。
func (d *Dialer) SetUDPTransport(t *Transport) { d.udpTransport.Store(t) }

// SetRouter 原子替换当前分流脑(用于列表刷新后的热重载)。
func (d *Dialer) SetRouter(r *route.Router) { d.router.Store(r) }

func network(udp bool) string {
	if udp {
		return "udp"
	}
	return "tcp"
}

// killswitchBlocks 报告 kill-switch 是否应拦截经传输 t 的代理连接。
// fail-closed:killswitch 开,且(无健康信号 Healthy==nil 或 明确不健康)→ 拦截。
// 无健康信号视为「不可证明隧道在」——宁断不漏真实 IP(防御纵深:未接健康检查的新传输
// 在 killswitch 下也不会静默泄漏)。killswitch 关时永不拦(用户已显式接受可能泄漏)。
func (d *Dialer) killswitchBlocks(t *Transport) bool {
	if !d.Killswitch {
		return false
	}
	return t.Healthy == nil || !t.Healthy()
}

// Dial 处理一条来自 TUN 的连接,返回到出口的 net.Conn。
func (d *Dialer) Dial(ctx context.Context, m route.Meta) (net.Conn, error) {
	return d.DialWithInitial(ctx, m, nil)
}

// DialWithInitial 可用 TCP 首包中的 TLS SNI / HTTP Host 为未知 fake-IP 恢复域名。
func (d *Dialer) DialWithInitial(ctx context.Context, m route.Meta, initial []byte) (net.Conn, error) {
	rt := d.router.Load()
	tr := d.transport.Load()
	if tr == nil {
		// 未 SetTransport(理论不发生:run.go 启动即设、且早于开始服务)。仅防 nil 解引用 panic。
		// 此空传输 Healthy==nil:killswitch 开时 killswitchBlocks 视作 fail-closed → Proxy 决策
		// 阻断(不再走 nil Proxy.DialContext 而 panic),Direct/Block 决策仍安全。
		tr = &Transport{}
	}
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
			// 隧道挂 + kill-switch:直连 UDP 的「牺牲匿名换低延迟」只在代理正常工作时被接受。
			// 隧道不健康时整体隐私态已降级,宁可 fail-closed 断 UDP,也不暴露真实 IP。
			if d.killswitchBlocks(tr) {
				if d.Stats != nil {
					d.Stats.Blocked()
					d.Stats.UDPBlocked()
				}
				debugf("udp direct-realtime blocked (killswitch, tunnel down): ip=%s port=%d", m.IP, m.Port)
				return nil, ErrBlocked
			}
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
			// 首次直连打一次显著告警:此模式以真实 IP 直连所有 UDP(含境外 QUIC/HTTP3),
			// 牺牲匿名换低延迟 —— 让真实 IP 外泄可见可审计,而非静默发生。
			if d.leakWarned.CompareAndSwap(false, true) {
				log.Printf("⚠ direct-realtime: 所有 UDP(含境外 QUIC/HTTP3)以真实 IP 直连,牺牲匿名换低延迟")
			}
			target := net.JoinHostPort(ip.String(), strconv.Itoa(int(m.Port)))
			debugf("udp direct-realtime: ip=%s target=%s", m.IP, target)
			return d.Direct.DialContext(ctx, "udp", target)
		}
		if d.UDPMode == "proxy" {
			// 按类分流:UDP 优先走 UDP 专用传输(hysteria);未设则用主传输。
			utr := d.udpTransport.Load()
			if utr == nil {
				utr = tr
			}
			if d.killswitchBlocks(utr) {
				if d.Stats != nil {
					d.Stats.Blocked()
					d.Stats.UDPBlocked()
				}
				return nil, ErrBlocked // UDP 传输挂 → fail-closed,绝不回落
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
			return utr.Proxy.DialContext(ctx, "udp", target)
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
		if d.killswitchBlocks(tr) {
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
		conn, err := tr.Proxy.DialContext(ctx, network(m.UDP), target)
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
