package dialer

import (
	"net/netip"

	"github.com/getbx/bx/internal/route"
	"github.com/getbx/bx/internal/udpsource"
)

// Effective 是「现在向这个目标发一条连接,实际会发生什么」。
//
// 它与 route.Decision **不是一回事**:后者只是路由判定,而实际结局还要叠上
// kill-switch(隧道健康)与 UDP 那一档。一条判定为 Proxy 的连接在隧道不健康时
// 会被 Block —— 而「为什么我的请求失败了」几乎总是这一层的答案。
//
// 零值是 EffectiveUnknown:与本仓库其它判据同一条纪律,**零值必须是最不自信的
// 那个答案**,漏填是「说不知道」而不是「说它会走隧道」。
type Effective int

const (
	EffectiveUnknown Effective = iota
	EffectiveTunnel
	EffectiveDirect
	EffectiveBlocked
	EffectiveVia
)

func (e Effective) String() string {
	switch e {
	case EffectiveTunnel:
		return "tunnel"
	case EffectiveDirect:
		return "direct"
	case EffectiveBlocked:
		return "blocked"
	case EffectiveVia:
		return "via"
	default:
		return "unknown"
	}
}

// TunnelHealth 三态。
//
// **「没有健康探针」不是「健康」也不是「不健康」** —— 但 kill-switch 按不健康
// 处理(fail-closed)。把它压成 unhealthy 会让「传输还没装好」看起来像「隧道断了」,
// 压成 healthy 则是把 fail-closed 说成 fail-open。
type TunnelHealth int

const (
	TunnelHealthUnknown TunnelHealth = iota
	TunnelHealthy
	TunnelUnhealthy
)

func (h TunnelHealth) String() string {
	switch h {
	case TunnelHealthy:
		return "healthy"
	case TunnelUnhealthy:
		return "unhealthy"
	default:
		return "unknown"
	}
}

// Outcome 是一次 Explain 的答案。
type Outcome struct {
	// Domain/IP 是**判定时实际用的**那一对 —— Domain 可能来自 fake-IP 反查。
	Domain string
	IP     netip.Addr
	UDP    bool
	// FakeIPResolved 为真表示 Domain 是从假 IP 反查回来的。
	// 假 IP 查不到域名时判定会完全不同(退化成按 IP 判),这件事要说出来。
	FakeIPResolved bool

	// Decision/Reason 是**路由**判定与它的依据,Reason.Rule 是配置里那一行的原文。
	Decision route.Decision
	Reason   route.Reason

	// Effective 是叠上 kill-switch 与 UDP 档之后的结局。
	Effective Effective
	// BlockedBy 只在 Effective==Blocked 时非空:killswitch / udp_mode_block。
	BlockedBy string
	// Source 是这条连接会被记在哪个计数来源名下(udpsource.* 或 Reason.Source)。
	// 它让 explain 的答案与 `bx status` 里的那张表对得上。
	Source string
	// Egress 只在 Effective==Via 时非空。
	Egress string

	TunnelHealth TunnelHealth
	// UDPTransportHealth 是 UDP 专用传输的健康;没配时为 Unknown。
	UDPTransportHealth TunnelHealth
	// RouterMissing 为真表示还没有路由表可问 —— 这时候什么都答不了,
	// 而**答不了必须说出来**,不能让零值读起来像一个判定。
	RouterMissing bool
}

const (
	blockedByKillswitch = "killswitch"
	blockedByUDPMode    = "udp_mode_block"
)

func healthOf(t *Transport) TunnelHealth {
	if t == nil || t.Healthy == nil {
		return TunnelHealthUnknown
	}
	if t.Healthy() {
		return TunnelHealthy
	}
	return TunnelUnhealthy
}

// Explain 回答「现在向 m 发一条连接会发生什么、为什么」,**不发任何连接、不记任何账**。
//
// 它是 dialInner 的只读兄弟:同一个 Dialer 实例上的方法,读的是同一个 Router
// 指针、同一批 Transport、同一个 Killswitch/UDPMode 字段,并且调用**同一个**
// killswitchBlocks。**这不是第二份判据,是同一个对象的另一个问法** —— 在 CLI
// 里照着配置重建一个 Router 来回答才是第二份,而 bx 不热重载,盘上的配置与跑着
// 的那个不一致的那一刻,恰恰是最需要这个命令的那一刻。
//
// 分支结构与 dialInner 逐支对应,漂移由 explain_drift_test.go 挡住:那条测试
// 拿同一张输入表,比对 Explain 的预言与 dialInner **真的做出来**的结局。
func (d *Dialer) Explain(m route.Meta) Outcome {
	rt := d.router.Load()
	tr, udpTransport := d.loadTransportGeneration()
	if tr == nil {
		// 与 dialInner 同款:空传输的 Healthy==nil,kill-switch 按 fail-closed 处理。
		tr = &Transport{}
	}

	out := Outcome{UDP: m.UDP, IP: m.IP, TunnelHealth: healthOf(tr)}
	if udpTransport != nil {
		out.UDPTransportHealth = healthOf(udpTransport)
	}

	// ① fake-IP 反查。**刻意不做首包 sniff** —— 那要一条真连接的头几个字节,
	// 而 explain 手上没有;报「假 IP 查不到域名」比编一个域名诚实。
	if m.Domain == "" && d.Fake != nil {
		if dom, ok := d.Fake.Domain(m.IP); ok {
			m.Domain, out.FakeIPResolved = dom, true
		}
	}
	out.Domain = m.Domain

	if rt == nil {
		out.RouterMissing = true
		return out
	}

	if m.UDP {
		return d.explainUDP(rt, m, tr, udpTransport, out)
	}
	return d.explainTCP(rt, m, tr, out)
}

func (d *Dialer) explainTCP(rt *route.Router, m route.Meta, tr *Transport, out Outcome) Outcome {
	// 与 dialInner 的 switch 逐支对应:具名出口最先说话,然后是 split-DNS 强制直连。
	switch {
	case m.IP.IsValid() && rt.EgressFor(m.IP) != "":
		out.Decision = route.Via
		out.Reason = route.Reason{Source: route.SourceUserEgress, Rule: rt.EgressFor(m.IP)}
	case m.Domain == "" && d.SplitDirect != nil && d.SplitDirect.Contains(m.IP):
		out.Decision = route.Direct
		out.Reason = route.Reason{Source: route.SourceSplitDNS}
	default:
		out.Decision, out.Reason = rt.Explain(m)
	}
	out.Source = out.Reason.Source.String()

	switch out.Decision {
	case route.Via:
		out.Effective, out.Egress = EffectiveVia, out.Reason.Rule
	case route.Direct:
		// 直连不受 kill-switch 约束:它防的是隧道挂掉时**回落**直连泄漏真实 IP,
		// 而这里是用户点名要直连。
		out.Effective = EffectiveDirect
	case route.Proxy:
		if d.killswitchBlocks(tr) {
			out.Effective, out.BlockedBy = EffectiveBlocked, blockedByKillswitch
		} else {
			out.Effective = EffectiveTunnel
		}
	default:
		out.Effective = EffectiveBlocked
	}
	return out
}

func (d *Dialer) explainUDP(rt *route.Router, m route.Meta, tr, udpTransport *Transport, out Outcome) Outcome {
	// ① 用户规则 / 私网:UDP 与 TCP 走同一条路(dialUDPByRule)。
	if dec, why, ok := udpRuleOverrideDecision(rt, m); ok {
		out.Decision, out.Reason, out.Source = dec, why, why.Source.String()
		switch dec {
		case route.Via:
			out.Effective, out.Egress = EffectiveVia, why.Rule
		case route.Direct:
			out.Effective = EffectiveDirect
		default:
			// 用户点名走隧道:fail-closed,且先试 UDP 专用传输再回落主传输。
			if d.killswitchBlocks(preferredUDPTransport(tr, udpTransport)) {
				out.Effective, out.BlockedBy = EffectiveBlocked, blockedByKillswitch
			} else {
				out.Effective = EffectiveTunnel
			}
		}
		return out
	}
	// ② 没有用户规则:路由判定仍然报出来(它是反事实计数的那一半),
	//    但结局由 udp.mode 决定 —— 这两件事**不许合并**。
	out.Decision, out.Reason = rt.Explain(m)

	switch d.UDPMode {
	case "direct-realtime":
		out.Source = udpsource.DirectRealtime
		if d.killswitchBlocks(tr) {
			out.Effective, out.BlockedBy = EffectiveBlocked, blockedByKillswitch
		} else {
			out.Effective = EffectiveDirect
		}
	case "proxy":
		utr := preferredUDPTransport(tr, udpTransport)
		out.Source = udpsource.Proxy
		if utr == tr {
			out.Source = udpsource.ProxyFallback
		}
		if d.killswitchBlocks(utr) {
			out.Effective, out.BlockedBy = EffectiveBlocked, blockedByKillswitch
		} else {
			out.Effective = EffectiveTunnel
		}
	default:
		out.Source = udpsource.ModeBlock
		out.Effective, out.BlockedBy = EffectiveBlocked, blockedByUDPMode
	}
	return out
}

// preferredUDPTransport 复刻 dialInner 里那句「专用传输不健康就回落主传输」。
func preferredUDPTransport(main, udp *Transport) *Transport {
	if udp == nil || udp.Healthy == nil || !udp.Healthy() {
		return main
	}
	return udp
}
