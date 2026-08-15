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

// DecisionCounter 按分流**决策与结果**计数(由 stats.Counters 实现)。
//
// **结果那一半是后加的,而且刻意加进同一个接口而不是做成可选断言。**
// 可选断言意味着「实现里没有就静默不计」—— 一个悄悄不工作的计数器,
// 与没有这个功能在输出上完全一样(都是 0),而它恰恰是用来发现「有东西
// 在悄悄失败」的。放进接口,编译器会逼每个实现表态。
type DecisionCounter interface {
	Proxy()
	Direct()
	Blocked()
	UDPBlocked()

	// DirectFailed / ProxyFailed 记一次拨号失败。bx 就在数据面上,
	// 这些失败它每一次都看见 —— 此前只打进 debug 日志然后扔掉。
	DirectFailed()
	ProxyFailed()

	// RuleAttempt / RuleFailure 把判定与失败归因到**做出判定的那条规则**。
	// 用户规则传原文(config 里那一行),内建列表传空串。
	RuleAttempt(source, rule string)
	RuleFailure(source, rule string)
}

// Dialer 把 Router 决策落到实际拨号。
type Dialer struct {
	router     atomic.Pointer[route.Router]
	transports atomic.Pointer[transportGeneration]
	Fake       *fakeip.Pool // 可空:无 fake-IP 时按 IP 直判
	Resolver   Resolver
	Direct     ContextDialer // 直连
	Killswitch bool
	Stats      DecisionCounter // 可空:决策计数
	UDPMode    string          // proxy(默认,走隧道), direct-realtime(直连真实 IP), block
	// SplitDirect 可空:split-DNS 解析出的内网真实 IP 集,命中即强制直连(绕 Router)。
	SplitDirect *splitdns.Set

	leakWarned atomic.Bool // direct-realtime 真实 IP 外泄告警只打一次
}

// Transport 是一次可原子替换的传输(socks 代理 + 健康判定),供运行期换隧道。
type Transport struct {
	Proxy   ContextDialer // 经隧道 socks5
	Healthy func() bool   // 隧道健康(kill-switch 用);可空
}

type transportGeneration struct {
	main *Transport
	udp  *Transport
}

// SetTransportGeneration 把主传输和可选 UDP 传输作为同一 generation 一次发布。
func (d *Dialer) SetTransportGeneration(main, udp *Transport) {
	d.transports.Store(&transportGeneration{main: main, udp: udp})
}

// SetTransport 原子替换主传输并保留当前 UDP slot。
func (d *Dialer) SetTransport(t *Transport) {
	for {
		current := d.transports.Load()
		var udp *Transport
		if current != nil {
			udp = current.udp
		}
		next := &transportGeneration{main: t, udp: udp}
		if d.transports.CompareAndSwap(current, next) {
			return
		}
	}
}

// SetUDPTransport 设 UDP 专用传输(按类分流的速度档,如 hysteria2);nil 则 UDP 走主传输。
// 不变量:该传输不健康时 UDP proxy 仍 fail-closed Block,绝不回落直连/主传输。
func (d *Dialer) SetUDPTransport(t *Transport) {
	for {
		current := d.transports.Load()
		var main *Transport
		if current != nil {
			main = current.main
		}
		next := &transportGeneration{main: main, udp: t}
		if d.transports.CompareAndSwap(current, next) {
			return
		}
	}
}

func (d *Dialer) loadTransportGeneration() (main, udp *Transport) {
	current := d.transports.Load()
	if current == nil {
		return nil, nil
	}
	return current.main, current.udp
}

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
	tr, udpTransport := d.loadTransportGeneration()
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
		// **先问 router 一次。** UDP 此前在这里就短路了,于是两件事被跳过:
		//
		// ① 用户自己写的 direct/proxy 规则 —— 加一条直连是一句「这些流量不要
		//    经过隧道」的指令,而 bx 只对 TCP 照办、对 UDP 当没听见,且不说。
		//    Apple/iCloud/Steam/微信这些最常见的直连目标恰恰重度用 QUIC。
		// ② **「私网恒直连」这条不变量** —— 局域网 UDP(mDNS、AirPlay、打印机、
		//    SMB 发现)被送进了隧道。这一条不是取舍,是 bug。
		//
		// **china 列表与默认行为刻意不在此列**:那是 bx 塞进去的 12165 条默认,
		// 用户没有逐条看过;把它的作用域从 TCP 悄悄扩到 UDP,等于一次性把一大批
		// 流量推到真实 IP 上出去。那要单独决定,不搭这趟车。
		if dec, why, ok := d.udpRuleOverride(m); ok {
			return d.dialUDPByRule(ctx, m, dec, why, tr, udpTransport)
		}
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
				d.Stats.RuleAttempt(udpSourceDirectRealtime, "")
			}
			ip := m.IP
			if m.Domain != "" {
				resolved, err := d.Resolver.Resolve(ctx, m.Domain)
				if err != nil {
					// 解析失败也是这条路的失败。**此前它直接返回,一次都没被数过** ——
					// 一条每次都解析不出来的 UDP 路径在 bx status 里完全隐形。
					d.recordUDPFailure(route.Direct, udpSourceDirectRealtime, "")
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
			conn, err := d.Direct.DialContext(ctx, "udp", target)
			if err != nil {
				d.recordUDPFailure(route.Direct, udpSourceDirectRealtime, "")
			}
			return conn, err
		}
		if d.UDPMode == "proxy" {
			// 按类分流:UDP 优先走专用传输(hysteria);它未设或不健康 → 回落主传输(reality)。
			// hys2 与主传输去的是同一台 VPS、同一条加密隧道,故回落它 ≠ 回落直连、不泄漏——
			// killswitch 真正要防的是回落直连暴露真实 IP。hys2 是纯加速档:好用时走它、挂了
			// 自动退到 reality 保通路,绝不黑洞 UDP。只有主传输也挂,下面的 killswitch 才 Block。
			// **回落要留痕。** 专用 UDP 传输挂掉时这里静默退回主传输 —— 通路保住了,
			// 而用户白白失去了 QUIC 加速档,且此前没有任何地方说得出这件事正在发生。
			utr, source := udpTransport, udpSourceProxy
			if utr == nil || utr.Healthy == nil || !utr.Healthy() {
				utr, source = tr, udpSourceProxyFallback
			}
			if d.killswitchBlocks(utr) {
				if d.Stats != nil {
					d.Stats.Blocked()
					d.Stats.UDPBlocked()
				}
				return nil, ErrBlocked // 主传输也挂 → fail-closed(仍绝不回落直连)
			}
			if d.Stats != nil {
				d.Stats.Proxy()
				d.Stats.RuleAttempt(source, "")
			}
			host := m.Domain
			if host == "" {
				host = m.IP.String()
			}
			target := net.JoinHostPort(host, strconv.Itoa(int(m.Port)))
			debugf("udp proxy: ip=%s domain=%q target=%s", m.IP, m.Domain, target)
			conn, err := utr.Proxy.DialContext(ctx, "udp", target)
			if err != nil {
				d.recordUDPFailure(route.Proxy, source, "")
			}
			return conn, err
		}
		if d.Stats != nil {
			d.Stats.Blocked()
			d.Stats.UDPBlocked()
		}
		debugf("udp blocked: ip=%s domain=%q port=%d", m.IP, m.Domain, m.Port)
		return nil, ErrBlocked
	}

	var dec route.Decision
	var why route.Reason
	if m.Domain == "" && d.SplitDirect != nil && d.SplitDirect.Contains(m.IP) {
		dec = route.Direct // split 解析出的内网真实 IP:强制直连,跳过 Router
		why = route.Reason{Source: route.SourceSplitDNS}
	} else {
		dec, why = rt.Explain(m)
	}

	// 2) 未命中域名:用国内 DNS 解析后按 IP 二次判定
	var resolved netip.Addr
	if dec == route.NeedResolve {
		ip, err := d.Resolver.Resolve(ctx, m.Domain)
		if err != nil {
			dec = route.Proxy // 解析失败保守走代理
			why = route.Reason{Source: route.SourceDefault}
		} else {
			resolved = ip
			dec, why = rt.ExplainIP(ip)
		}
	}

	port := strconv.Itoa(int(m.Port))
	switch dec {
	case route.Direct:
		if d.Stats != nil {
			d.Stats.Direct()
			d.Stats.RuleAttempt(why.Source.String(), why.Rule)
		}
		var target string
		if m.Domain != "" {
			ip := resolved
			if !ip.IsValid() {
				r, err := d.Resolver.Resolve(ctx, m.Domain)
				if err != nil {
					// **解析不出来也是这条规则的失败。** 少算它会让一条把域名
					// 逼向坏解析器的规则看起来毫无问题。
					d.recordFailure(route.Direct, why)
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
			d.recordFailure(route.Direct, why)
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
			d.Stats.RuleAttempt(why.Source.String(), why.Rule)
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
			d.recordFailure(route.Proxy, why)
		}
		return conn, err

	default: // Block
		if d.Stats != nil {
			d.Stats.Blocked()
		}
		return nil, ErrBlocked
	}
}

// udpRuleOverride 问 router:这条 UDP 有没有被**用户规则**或**私网不变量**点名。
//
// 返回 false 表示没有 —— 那时仍由 udp.mode 决定(隧道 / 真实 IP 直连 / 阻断)。
// 判据刻意只认这两类:`IsUserRule` 是用户自己下的判断,`SourcePrivate` 是本项目
// 明写的不变量;china 列表与默认落在外面。
func (d *Dialer) udpRuleOverride(m route.Meta) (route.Decision, route.Reason, bool) {
	r := d.router.Load()
	if r == nil {
		return 0, route.Reason{}, false
	}
	dec, why := r.Explain(m)
	if why.Source.IsUserRule() || why.Source == route.SourcePrivate {
		return dec, why, true
	}
	d.countUDPCounterfactual(m, why)
	return 0, route.Reason{}, false
}

// UDP 的**反事实**计数:如果让 china 列表也对 UDP 生效(即那个被搁置的选项),
// 有多少连接会从隧道改走直连?
//
// **注意这几行与判定行住在同一张表里,而它们不是判定。** 同一条 UDP 连接会贡献
// 两行:一行 udp_proxy* (它实际走了哪儿),一行 udp_would_flip_*/udp_no_change
// (如果换个规矩它会走哪儿)。**两类行不许相加** —— 相加得到的是连接数的两倍。
// 消费方按 Source 前缀各取各的(见 stats.UDPStats 的 default: continue)。
//
// **这几个桶只计数,不改变任何行为。** 它们存在是为了让那个决定有数据可依,
// 而不是靠推测 —— 这个仓库里凡是「听起来显然」而没人去量的判断,后来都错了。
//
// **代价为零**:Explain 每条 UDP 本来就调了(上面那一句),完整判定算出来之后
// 只留下用户规则与私网两类,其余原本直接丢弃。这里只是把丢掉的那半记下来。
const (
	// udpWouldFlipDomain:有域名且命中 china 域名列表。
	udpWouldFlipDomain = "udp_would_flip_domain"
	// udpWouldFlipCIDR:没有域名、地址是真实公网 IP 且命中 china CIDR。
	udpWouldFlipCIDR = "udp_would_flip_cidr"
	// udpUndecidable:没有域名,而地址落在 fake-IP 段里 —— **判不出来**。
	//
	// 这一桶是要害。无域名时 Explain 走 ExplainIP,而 fake IP 既不在 china CIDR
	// 也不在私网段,会落进「默认」。把它折进「不受影响」就是把「问不出来」报成
	// 一个具体答案 —— 而它恰恰会让影响面被系统性低估。
	udpUndecidable = "udp_undecidable"
	// udpNoChange:其余(境外、默认)。**必须也记**,否则上面几个数没有分母。
	udpNoChange = "udp_no_change"
)

func (d *Dialer) countUDPCounterfactual(m route.Meta, why route.Reason) {
	if d.Stats == nil {
		return
	}
	// **不能直接把 d.Fake 传进去。** 它是 *fakeip.Pool;为 nil 时装进接口会得到
	// 一个「非 nil 的接口值包着一个 nil 指针」,于是被调方那句 `pool != nil`
	// 为真、随即在解引用时 panic。Go 里最经典的那个坑,而它在这里的后果是
	// **没有 fake-IP 的部署一发 UDP 就崩**。
	var pool fakeIPRange
	if d.Fake != nil {
		pool = d.Fake
	}
	d.Stats.RuleAttempt(udpCounterfactualBucket(m, why, pool), "")
}

// udpCounterfactualBucket 是**纯判定**,单独抽出来是为了逐种输入可测 ——
// 尤其是「判不出来」那一桶,它按构造在生产里最难造出来,也最容易被写错。
//
// pool 可为 nil(没有 fake-IP 时):那时地址就是真实地址,不存在判不出来。
func udpCounterfactualBucket(m route.Meta, why route.Reason, pool fakeIPRange) string {
	if m.Domain != "" {
		if why.Source == route.SourceChinaDomain {
			return udpWouldFlipDomain
		}
		return udpNoChange
	}
	// 没有域名:先问这个地址是不是一个我们本该认识、却反查不到的 fake IP。
	if pool != nil && m.IP.IsValid() && pool.Contains(m.IP) {
		return udpUndecidable
	}
	if why.Source == route.SourceChinaCIDR {
		return udpWouldFlipCIDR
	}
	return udpNoChange
}

// fakeIPRange 只要「这个地址归不归 fake-IP 池管」这一个问题。
// 收窄成一个单方法接口,是为了让判定在单测里造得出来。
type fakeIPRange interface {
	Contains(netip.Addr) bool
}

// dialUDPByRule 按用户规则(或私网不变量)拨一条 UDP。
//
// **归因用真实的规则来源,不另造一个 udp_ 前缀**:这样用户那条 config 里的规则
// 在 `bx status` 里连 UDP 的成败一起认领 —— 点名到的是他改得了的那一行,而
// 「同一条规则的 TCP 与 UDP 分列两处」只会让人以为有两个问题。
func (d *Dialer) dialUDPByRule(ctx context.Context, m route.Meta, dec route.Decision, why route.Reason, tr, udpTransport *Transport) (net.Conn, error) {
	if dec == route.Direct {
		// **直连不受 kill-switch 约束**,与 TCP 的直连同理:kill-switch 防的是
		// 隧道挂掉时**回落**直连泄漏真实 IP,而这里是用户点名要直连。
		if d.Stats != nil {
			d.Stats.Direct()
			d.Stats.RuleAttempt(why.Source.String(), why.Rule)
		}
		ip := m.IP
		if m.Domain != "" {
			resolved, err := d.Resolver.Resolve(ctx, m.Domain)
			if err != nil {
				d.recordUDPFailure(route.Direct, why.Source.String(), why.Rule)
				return nil, err
			}
			ip = resolved
		}
		target := net.JoinHostPort(ip.String(), strconv.Itoa(int(m.Port)))
		debugf("udp direct by rule: source=%s rule=%q target=%s", why.Source, why.Rule, target)
		conn, err := d.Direct.DialContext(ctx, "udp", target)
		if err != nil {
			d.recordUDPFailure(route.Direct, why.Source.String(), why.Rule)
		}
		return conn, err
	}

	// 用户点名走隧道:**fail-closed**。这一条在 udp.mode=direct-realtime 下尤其
	// 要紧 —— 那个模式让所有 UDP 以真实 IP 出去,包括用户明确标了强制走隧道的
	// 域名,而那是直接违背一条显式指令的泄漏。
	utr := udpTransport
	if utr == nil || utr.Healthy == nil || !utr.Healthy() {
		utr = tr
	}
	if d.killswitchBlocks(utr) {
		if d.Stats != nil {
			d.Stats.Blocked()
			d.Stats.UDPBlocked()
		}
		return nil, ErrBlocked
	}
	if d.Stats != nil {
		d.Stats.Proxy()
		d.Stats.RuleAttempt(why.Source.String(), why.Rule)
	}
	host := m.Domain
	if host == "" {
		host = m.IP.String()
	}
	target := net.JoinHostPort(host, strconv.Itoa(int(m.Port)))
	debugf("udp proxy by rule: source=%s rule=%q target=%s", why.Source, why.Rule, target)
	conn, err := utr.Proxy.DialContext(ctx, "udp", target)
	if err != nil {
		d.recordUDPFailure(route.Proxy, why.Source.String(), why.Rule)
	}
	return conn, err
}

// UDP 的三条来源。
//
// **UDP 根本不问 router**(见 DialContext 里 m.UDP 那段:proxy 模式下所有 UDP
// 一律走隧道),所以它没有「命中了哪条规则」可归因。但它必须归因到**某个东西** ——
// 否则 `bx status` 里那几百条 proxy 连接凭空出现、无从解释,而这正是「结果计数」
// 这件事要消灭的处境。归到「模式」是这条路上唯一诚实的答案。
const (
	udpSourceProxy          = "udp_proxy"
	udpSourceProxyFallback  = "udp_proxy_fallback"
	udpSourceDirectRealtime = "udp_direct_realtime"
)

// recordUDPFailure 记一次 UDP 拨号失败。
//
// **UDP 的失败此前一次都没被数过** —— recordFailure 只在 TCP 那三条路上可达,
// 而 UDP 分支在 DialContext 顶部就短路了。于是一条 UDP 传输哪怕每次拨号都失败,
// `bx status` 里也是一片安静:proxy 计数照涨(判定发生了),failed 恒为 0。
func (d *Dialer) recordUDPFailure(dec route.Decision, source, rule string) {
	if d.Stats == nil {
		return
	}
	switch dec {
	case route.Direct:
		d.Stats.DirectFailed()
	case route.Proxy:
		d.Stats.ProxyFailed()
	default:
		return
	}
	d.Stats.RuleFailure(source, rule)
}

// recordFailure 记一次拨号失败:总数一份,按规则归因一份。
//
// 两份都要:总数回答「现在整体坏得厉害吗」,归因回答「该改哪一行」。
// 只有前者时用户看得见有问题却找不到源头 —— 那正是这次 Steam 排查的处境。
func (d *Dialer) recordFailure(dec route.Decision, why route.Reason) {
	if d.Stats == nil {
		return
	}
	switch dec {
	case route.Direct:
		d.Stats.DirectFailed()
	case route.Proxy:
		d.Stats.ProxyFailed()
	default:
		return
	}
	d.Stats.RuleFailure(why.Source.String(), why.Rule)
}
