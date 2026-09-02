package supervisor

import (
	"errors"
	"net/netip"
	"strings"

	"github.com/getbx/bx/internal/dialer"
	"github.com/getbx/bx/internal/route"
	"github.com/getbx/bx/internal/stats"
)

// ExplainResponse 是 GET /v0/explain 的响应体。
//
// **TCP 与 UDP 分开发布,绝不合并**:udp.transport / udp.mode 意味着同一个目的地
// 的 UDP 可能去往和 TCP 完全不同的地方(甚至一个走隧道一个被 Block)。压成一个
// 答案会把这个事实整个抹掉 —— 与 appattr.PortKey{Port, UDP} 同一条纪律:
// **协议维度不能省**,省掉的后果是把一件事记在另一件事名下。
type ExplainResponse struct {
	// Target 是调用方原样传进来的串,回显它让人确认自己问的是什么。
	Target string `json:"target"`
	// Domain/IP 是**判定时实际用的**那一对。Domain 可能来自假 IP 反查。
	Domain string `json:"domain,omitempty"`
	IP     string `json:"ip,omitempty"`
	// FakeIPResolved 为真 = Domain 是从假 IP 反查回来的。假 IP 查不到域名时
	// 判定会完全不同(退化成按 IP 判),这件事必须说出来。
	FakeIPResolved bool `json:"fake_ip_resolved"`

	TCP ExplainPath `json:"tcp"`
	UDP ExplainPath `json:"udp"`

	// TunnelHealth / UDPTransportHealth 三态串:healthy / unhealthy / unknown。
	// **unknown 不是 unhealthy** —— 它是「没有健康探针」,而 kill-switch 对它
	// 按不健康处理。压成 unhealthy 会把「传输还没装好」说成「隧道断了」。
	TunnelHealth       string `json:"tunnel_health"`
	UDPTransportHealth string `json:"udp_transport_health"`
	// RouterMissing 为真 = 还没有路由表可问,上面两个 ExplainPath 什么都不说明。
	// **刻意无 omitempty**:false 与「这一版没有这个字段」必须分得开。
	RouterMissing bool `json:"router_missing"`

	// HistoryWindow 是**累计计数覆盖多长时间**(Core 累计在跑的秒数),
	// HistoryVersions 是这段累积跨过的版本。
	//
	// 少了它们,「累计 2679 次 / 410 次失败」答不出「这是什么时候的事」——
	// 一个跨了半年、几个版本的 15% 与一天之内的 15% 是完全不同的两件事,
	// 而读的人会默认它是后者。两个字段本来就在 RuleHistorySnapshot 里,
	// 此前被整个丢掉了。
	HistoryWindowSeconds int64    `json:"history_window_seconds,omitempty"`
	HistoryVersions      []string `json:"history_versions,omitempty"`
	// HistoryOverflowed 为真时,「这条规则没有条目」不再等于「它没命中过」。
	HistoryOverflowed bool `json:"history_overflowed"`
}

// ExplainPath 是一个协议方向上的答案。
type ExplainPath struct {
	// Effective 是叠上 kill-switch 与 UDP 档之后的**实际结局**:
	// tunnel / direct / blocked / via / unknown。
	Effective string `json:"effective"`
	// Decision 是**路由**判定,与 Effective 不是一回事 —— 一条判定为 proxy 的
	// 连接在隧道不健康时实际会被 blocked,而「我的请求为什么失败」几乎总是
	// 这一层的答案。两个都发,让读的人看得见那一跳。
	Decision string `json:"decision"`
	// Source 是这条连接会被记在哪个计数来源名下,让本命令的答案与
	// `bx status` 里那张规则表对得上。
	Source string `json:"source"`
	// Rule 是命中的用户规则**原文**(config 里那一行);内建列表命中时为空 ——
	// 报一个用户搜不到的串不如不报。
	Rule string `json:"rule,omitempty"`
	// BlockedBy 只在 Effective==blocked 时非空:killswitch / udp_mode_block。
	BlockedBy string `json:"blocked_by,omitempty"`
	Egress    string `json:"egress,omitempty"`

	// Run 是**本次运行**里这条规则的成败;History 是**跨重启累计**。
	// 两者并列,绝不合并 —— 「0 次」在本次运行里什么也说明不了(刚重连的
	// 机器上每条规则都是 0),而那正是 stats.Report 已经确立的纪律。
	// nil = 没有这条规则的记录,与「记录为 0」是两件事。
	Run     *stats.RuleOutcome `json:"run,omitempty"`
	History *stats.RuleOutcome `json:"history,omitempty"`
}

var errEmptyExplainTarget = errors.New("explain target is required")

// ErrExplainUnsupported 是「这一版 Core 没有 /v0/explain」,**与「这个目标没有
// 答案」是两件事** —— 压成一句「请求失败」会把人送去查一个不存在的网络问题。
var ErrExplainUnsupported = errors.New("this Core does not publish /v0/explain")

// explainTarget 把一个用户输入的目标解析成 route.Meta。
//
// **纯函数,不做任何 DNS**:域名原样带走,由 Router 按域名判(route.Explain 对
// 域名从不解析 —— 未命中列表时直接默认走代理,注释里写明了理由是不拿可能被
// 污染的国内 DNS 做 geoip)。IP 字面量走 IP 那一支。
//
// 端口只影响不了判定(Router 不看端口),但仍然解析并带上:它进 route.Meta 是
// 为了让这条查询与真连接**形状一致**,而不是让判据多一个输入。
func explainTarget(target string) (route.Meta, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return route.Meta{}, errEmptyExplainTarget
	}
	host, port := target, uint16(443)
	if h, p, ok := splitHostPort(target); ok {
		host, port = h, p
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return route.Meta{IP: ip, Port: port}, nil
	}
	// 尾点归一化:`example.com.` 与 `example.com` 是同一个域名,而 DomainSet
	// 按字面匹配 —— 不归一化会让用户查一个自己配置里明明写着的域名却报「没命中」。
	return route.Meta{Domain: strings.ToLower(strings.TrimSuffix(host, ".")), Port: port}, nil
}

// splitHostPort 只认**结尾**的 `:数字`,而且对裸 IPv6 保持沉默。
// `[::1]:53` 走方括号那一支;`::1` 不许被切成 host="::" port=1。
func splitHostPort(s string) (string, uint16, bool) {
	if strings.HasPrefix(s, "[") {
		if end := strings.Index(s, "]"); end > 0 {
			host := s[1:end]
			if rest := s[end+1:]; strings.HasPrefix(rest, ":") {
				if p, ok := parsePort(rest[1:]); ok {
					return host, p, true
				}
			}
			return host, 443, true
		}
		return s, 0, false
	}
	idx := strings.LastIndex(s, ":")
	if idx <= 0 || strings.Count(s, ":") > 1 {
		return s, 0, false // 无端口,或裸 IPv6
	}
	p, ok := parsePort(s[idx+1:])
	if !ok {
		return s, 0, false
	}
	return s[:idx], p, true
}

func parsePort(s string) (uint16, bool) {
	if s == "" || len(s) > 5 {
		return 0, false
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	if n == 0 || n > 65535 {
		return 0, false
	}
	return uint16(n), true
}

// buildExplainResponse 把两个方向的 Outcome 与两张计数表拼成线材格式。
//
// **计数按 (source, rule) 配对查**,不只按 rule:同一条原文可以同时出现在
// direct 与 proxy 两张表里、语义相反,只按原文比会把另一半的成败算到这一半头上
// (与死规则判据那条「按 Source 把 direct/proxy 分开查」同源)。
func buildExplainResponse(target string, tcp, udp dialer.Outcome, rep stats.Report) ExplainResponse {
	out := ExplainResponse{
		Target:             target,
		Domain:             tcp.Domain,
		FakeIPResolved:     tcp.FakeIPResolved,
		TunnelHealth:       tcp.TunnelHealth.String(),
		UDPTransportHealth: udp.UDPTransportHealth.String(),
		RouterMissing:      tcp.RouterMissing,
		TCP:                explainPathOf(tcp, rep),
		UDP:                explainPathOf(udp, rep),
	}
	if tcp.IP.IsValid() {
		out.IP = tcp.IP.String()
	}
	if rep.RuleHistory != nil {
		out.HistoryWindowSeconds = rep.RuleHistory.UptimeSeconds
		out.HistoryVersions = rep.RuleHistory.Versions
		out.HistoryOverflowed = rep.RuleHistory.Overflowed
	}
	return out
}

func explainPathOf(o dialer.Outcome, rep stats.Report) ExplainPath {
	p := ExplainPath{
		Effective: o.Effective.String(),
		Decision:  o.Decision.String(),
		Source:    o.Source,
		Rule:      o.Reason.Rule,
		BlockedBy: o.BlockedBy,
		Egress:    o.Egress,
	}
	p.Run = findRuleOutcome(rep.Rules, o.Source, o.Reason.Rule)
	if rep.RuleHistory != nil {
		p.History = findRuleOutcome(rep.RuleHistory.Rules, o.Source, o.Reason.Rule)
	}
	return p
}

func findRuleOutcome(rules []stats.RuleOutcome, source, rule string) *stats.RuleOutcome {
	for i := range rules {
		if rules[i].Source == source && rules[i].Rule == rule {
			found := rules[i]
			return &found
		}
	}
	return nil
}
