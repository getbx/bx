package stats

import (
	"fmt"
	"sort"
)

// maxTrackedRules 是纯防御性上限。
//
// 真实配置里用户规则最多几十条,内建来源固定七八种 —— 正常永远撞不到。
// 撞到就说明有人在按**域名**而不是按**规则**计数,那会随流量无界增长;
// 上限让它退化成「少报几条」,而不是把 Core 的内存吃光。
const maxTrackedRules = 256

// RuleOutcome 是一条规则(或一个内建来源)的判定与结果。
//
// **Attempts 与 Failures 分开,是这整件事的要点。** bx 一直只有前者:
// 它答得出「26186 条走了直连」,答不出「其中 8113 条在 4 毫秒内失败」。
type RuleOutcome struct {
	// Source 是做出判定的那一层(user_direct / china_domain / default …)。
	Source string `json:"source"`
	// Rule 是命中的用户规则**原文**;内建列表命中时为空 ——
	// 报一个用户在 config 里搜不到的串不如不报。
	Rule     string `json:"rule,omitempty"`
	Attempts int64  `json:"attempts"`
	Failures int64  `json:"failures"`
}

type ruleKey struct{ source, rule string }

// RuleAttempt 记一次按此规则做出的判定。
func (c *Counters) RuleAttempt(source, rule string) { c.bump(source, rule, false) }

// RuleFailure 记一次按此规则拨号失败。
//
// **失败不重复计入 Attempts**:调用方对同一次连接先 RuleAttempt 再(失败时)
// RuleFailure,故 Failures ≤ Attempts 恒成立。反过来会让「全失败」看起来像半数失败。
func (c *Counters) RuleFailure(source, rule string) { c.bump(source, rule, true) }

func (c *Counters) bump(source, rule string, failed bool) {
	if source == "" {
		return
	}
	key := ruleKey{source: source, rule: rule}
	c.ruleMu.Lock()
	defer c.ruleMu.Unlock()
	if c.rules == nil {
		c.rules = make(map[ruleKey]*RuleOutcome)
	}
	entry := c.rules[key]
	if entry == nil {
		// 上限只挡**新增**:已在跟踪的规则继续正常计数,不会因为撞了上限
		// 就把已有的观测冻住 —— 那会让一条正在失败的规则悄悄停止上报。
		if len(c.rules) >= maxTrackedRules {
			return
		}
		entry = &RuleOutcome{Source: source, Rule: rule}
		c.rules[key] = entry
	}
	if failed {
		entry.Failures++
		return
	}
	entry.Attempts++
}

// ruleSnapshot 返回稳定排序的规则结果。
//
// 排序是必需的:map 迭代序随机,`bx status` 每次输出顺序都不同就 diff 不了,
// 而用户排查时做的第一件事往往就是前后对比两次输出。
func (c *Counters) ruleSnapshot() []RuleOutcome {
	c.ruleMu.Lock()
	defer c.ruleMu.Unlock()
	if len(c.rules) == 0 {
		return nil
	}
	out := make([]RuleOutcome, 0, len(c.rules))
	for _, entry := range c.rules {
		out = append(out, *entry)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Source != out[j].Source {
			return out[i].Source < out[j].Source
		}
		return out[i].Rule < out[j].Rule
	})
	return out
}

// 报告一条规则的门槛。**刻意保守**:这几行会出现在用户每一次 bx status 的输出里,
// 一旦开始出现无关紧要的规则,它就会和「Status Protected」一样被跳过不读 ——
// 而它唯一的价值是被读到的那一次。
const (
	// 绝对数门槛:1/1 失败是 100%,但什么也说明不了。
	ruleReportMinFailures = 5
	// 比例门槛:偶发失败(网络抖动、目标临时不可用)是常态,不是配置问题。
	ruleReportMinFailRate = 0.5
)

// ruleWorthReporting 判断这条规则是否值得单独点名。
//
// **只报用户规则。** 内建列表没有「哪一行」可点名,用户也改不了;内建列表整体
// 失败是另一类故障(隧道坏了),由失败总数那一行表达 —— 两者的处置完全不同,
// 混在一起报会让用户对着一条自己动不了的东西干瞪眼。
func ruleWorthReporting(r RuleOutcome) bool {
	if r.Rule == "" || r.Failures < ruleReportMinFailures || r.Attempts <= 0 {
		return false
	}
	return float64(r.Failures)/float64(r.Attempts) >= ruleReportMinFailRate
}

// FailingRules 返回值得点名的规则,失败最多的在前。
func (s Snapshot) FailingRules() []RuleOutcome {
	var out []RuleOutcome
	for _, r := range s.Rules {
		if ruleWorthReporting(r) {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Failures > out[j].Failures })
	return out
}

// UDP 那三条来源的名字。**与 internal/dialer 里的常量必须一致** ——
// 这是唯一一处跨包按字符串对齐的地方,由 TestUDPSourceNamesMatchTheDialer 钉住。
const (
	udpSourceProxy          = "udp_proxy"
	udpSourceProxyFallback  = "udp_proxy_fallback"
	udpSourceDirectRealtime = "udp_direct_realtime"
)

// UDPOutcome 把 UDP 那几条来源汇总成一句能行动的话所需的数字。
//
// **UDP 根本不问 router**,所以它没有「命中了哪条规则」;归因到模式是这条路上
// 唯一诚实的答案。而在此之前它连模式都没有 —— status 里那几百条 proxy 连接
// 凭空出现、无从解释。
type UDPOutcome struct {
	// Dedicated 是走专用 UDP 传输(hysteria2 加速档)的次数。
	Dedicated int64
	// Fallback 是加速档不健康、退回主传输的次数。**它单独一份是要点**:
	// 合进 Dedicated 的话,「一直在回落」与「一直好好的」在输出里逐字节相同。
	Fallback int64
	// DirectRealtime 是以真实 IP 直连的次数(udp.mode=direct-realtime)。
	DirectRealtime int64
	// Failures 是上面三者的失败合计。
	Failures int64
}

// UDPStats 汇总快照里的 UDP 来源。
func (s Snapshot) UDPStats() UDPOutcome {
	var out UDPOutcome
	for _, r := range s.Rules {
		switch r.Source {
		case udpSourceProxy:
			out.Dedicated += r.Attempts
		case udpSourceProxyFallback:
			out.Fallback += r.Attempts
		case udpSourceDirectRealtime:
			out.DirectRealtime += r.Attempts
		default:
			continue
		}
		out.Failures += r.Failures
	}
	return out
}

// UDPNotice 给出 UDP 这条路上**值得说的那句话**;没什么可说时返回空串。
//
// **一切正常时一个字都不打**,这是它不被训练成噪声的前提(与失败规则那一段
// 同一条纪律)。门槛也取同一套:绝对数 ≥5 且比例 ≥50% —— 1/1 失败是 100%
// 但什么也说明不了,而启动那一瞬的几次回落同样不值得占一行。
func (s Snapshot) UDPNotice() string {
	u := s.UDPStats()
	total := u.Dedicated + u.Fallback + u.DirectRealtime
	if total <= 0 {
		return ""
	}
	// 回落优先说:它有一个明确的动作(去看那台 UDP 服务器),而且用户完全
	// 感知不到 —— 通路是好的,只是慢。
	if u.Fallback >= ruleReportMinFailures &&
		float64(u.Fallback)/float64(u.Dedicated+u.Fallback) >= ruleReportMinFailRate {
		return fmt.Sprintf("UDP 加速档没在用:%d/%d 条回落到了主传输(那台 UDP 服务器不健康)",
			u.Fallback, u.Dedicated+u.Fallback)
	}
	if u.Failures >= ruleReportMinFailures &&
		float64(u.Failures)/float64(total) >= ruleReportMinFailRate {
		return fmt.Sprintf("UDP 拨号大量失败:%d/%d 条", u.Failures, total)
	}
	return ""
}
