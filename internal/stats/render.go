package stats

import (
	"fmt"
	"strings"
	"time"

	"github.com/getbx/bx/internal/elevate"
)

// Report 是 bx status 的线材格式:计数快照 + 隧道信息。
// Guardian 的非秘密交接状态由 supervisor.RuntimeState 单独承载,避免改变 /v0/status 契约。
type Report struct {
	Snapshot
	// RuleHistory 是**跨重启累计**的按规则计数,与 Snapshot 里 Rules[] 的
	// 「本次运行」计数**并列发布,绝不合并**。
	//
	// **合并会毁掉这个功能唯一想说的那句话**:「0 次」在本次运行里什么也说明不了
	// (一台刚重连的机器上每条规则都是 0 次),而死规则判据要的恰恰是累计值。
	// 两个数并排放着,读的人一眼就知道自己在看哪一个;合成一个数之后,那个区别
	// 再也表达不出来。
	//
	// **nil = 这一版 Core 没有这个概念,或者历史读不出来** —— 与「累计为 0」是
	// 两件事,故用指针 + omitempty,不能用零值结构代替。
	RuleHistory   *RuleHistorySnapshot `json:"rule_history,omitempty"`
	Server        string               `json:"server"`
	SocksAddr     string               `json:"socks_addr"`
	TunnelHealthy bool                 `json:"tunnel_healthy"`
	LatencyMS     int64                `json:"latency_ms"`
	// PeakBPS 是**观测到的**最高吞吐(上下行合计),0 = 这段时间没观测到。
	//
	// **0 与「跑不动」是两回事**,消费方必须分得开:一台整天没人用的服务器
	// 和一台带宽被打满到爬的服务器,在这里都是安静的 —— 只有前者是正常的。
	// 靠 PeakAt 缺席来表达「没观测到」(与本仓库里 Tristate 同一条纪律)。
	PeakBPS       int64     `json:"peak_bps,omitempty"`
	PeakAt        time.Time `json:"peak_at,omitempty"`
	Restarts      int       `json:"restarts"`
	Mode          string    `json:"mode,omitempty"` // 分流模式:split | global | router
	UDPMode       string    `json:"udp_mode"`
	UDPNote       string    `json:"udp_note,omitempty"`
	MutationState string    `json:"mutation_state,omitempty"`

	Transport    string    `json:"transport,omitempty"`     // 当前活跃传输 scheme@host(容灾后反映实际)
	Transports   []string  `json:"transports,omitempty"`    // 多传输容灾列表(>1 时,有序优先级)
	UDPTransport string    `json:"udp_transport,omitempty"` // UDP 专用传输(按类分流)
	Warnings     []Warning `json:"warnings,omitempty"`      // 运行期网络共存告警(只读检测)
	// ConfigPath 是**正在使用的**配置文件路径,由 supervisor 从 Options 填。
	// 点名一条坏规则时要告诉用户去哪改;stats 是叶子包,不该自己猜一份路径常量
	// (那会与 cli 那份 build-tagged 的默认值悄悄漂开)。
	ConfigPath string `json:"config_path,omitempty"`
}

// Warning 是 bx status 的轻量运行期告警,用于提示其他通道/系统代理等共存风险。
type Warning struct {
	Name     string `json:"name"`
	Severity string `json:"severity"`
	Detail   string `json:"detail"`
	Hint     string `json:"hint,omitempty"`
}

// modeLabel 给分流模式配一句说明,让 status 一眼看懂当前流量策略。
func modeLabel(mode string) string {
	switch mode {
	case "global":
		return "global (everything, China included, goes through the tunnel)"
	case "router":
		return "router (only forwarded LAN traffic is captured)"
	case "router-global":
		return "router · allowlist (forwarded LAN traffic all tunnelled, only the allowlist goes direct)"
	case "split":
		return "split (China direct / everything else through the tunnel)"
	default:
		return mode
	}
}

// statusLabelWidth 是面板左边那一列的宽度。
//
// **它是一个常量而不是每行手数的空格**,因为这块面板与 cli 那半
// (Status/Network/DNS/Hold/Loop)必须对齐成同一列 —— 手数的版本在中文时代
// 就已经差过一格(续行那句注释记着),而那种错没有任何测试会红。
const statusLabelWidth = 8

// Render 把 Report 渲染成命令行状态面板。
func Render(r Report) string {
	health := "● healthy"
	if !r.TunnelHealthy {
		health = "○ unhealthy"
	}
	ratio := r.ProxyRatio() * 100
	var b strings.Builder
	fmt.Fprintln(&b, "bx status")
	fmt.Fprintf(&b, "  %-*s%s  (socks %s)\n", statusLabelWidth, "Server", r.Server, r.SocksAddr)
	fmt.Fprintf(&b, "  %-*s%s  latency %dms  reconnects %d\n", statusLabelWidth, "Tunnel", health, r.LatencyMS, r.Restarts)
	if r.Mode != "" {
		fmt.Fprintf(&b, "  %-*s%s\n", statusLabelWidth, "Mode", modeLabel(r.Mode))
	}
	if r.Transport != "" {
		fmt.Fprintf(&b, "  %-*s%s", statusLabelWidth, "Via", transportWithoutRedundantHost(r.Transport, r.Server))
		if len(r.Transports) > 1 {
			fmt.Fprintf(&b, "  (failover %s)", strings.Join(r.Transports, " › "))
		}
		if r.UDPTransport != "" {
			fmt.Fprintf(&b, "  UDP→%s", transportWithoutRedundantHost(r.UDPTransport, r.Server))
		}
		fmt.Fprintln(&b)
	}
	fmt.Fprintf(&b, "  %-*sactive %d  proxy %d  direct %d  blocked %d\n",
		statusLabelWidth, "Conns", r.Active, r.Proxy, r.Direct, r.Blocked)
	udpMode := r.UDPMode
	if udpMode == "" {
		udpMode = "proxy"
	}
	fmt.Fprintf(&b, "  %-*smode %s  blocked %d", statusLabelWidth, "UDP", udpMode, r.UDPBlocked)
	if r.UDPNote != "" {
		fmt.Fprintf(&b, "  %s", r.UDPNote)
	}
	fmt.Fprintln(&b)
	// **失败与判定分开报,而且只在真有失败时才占一行。**
	// bx 就在数据面上,这些失败它每一次都看见 —— 此前只打进 debug 日志然后扔掉,
	// 于是「direct 26186」里藏着几千条秒失败的连接,而 status 一个字都不说。
	if r.DirectFailed > 0 || r.ProxyFailed > 0 {
		fmt.Fprintf(&b, "  %-*sproxy %d  direct %d\n", statusLabelWidth, "Failed", r.ProxyFailed, r.DirectFailed)
	}
	fmt.Fprintf(&b, "  %-*sproxy %.1f%% / direct %.1f%%\n", statusLabelWidth, "Split", ratio, 100-ratio)
	fmt.Fprintf(&b, "  %-*s↑ %s   ↓ %s\n", statusLabelWidth, "Traffic", HumanBytes(r.BytesUp), HumanBytes(r.BytesDown))
	// UDP 那条路的一句话。**一切正常时一个字都不打。**
	// 它此前完全隐形:UDP 不问 router,于是既没有规则归因,失败也一次都没被数过。
	if notice := r.UDPNotice(); notice != "" {
		fmt.Fprintf(&b, "  %-*s%s\n", statusLabelWidth, "UDP", notice)
	}
	// 点名成片失败的用户规则。**一切正常时这里一个字都不打** ——
	// 那是它不被训练成噪声的前提。
	if failing := r.FailingRules(); len(failing) > 0 {
		for i, rule := range failing {
			label := "Rule"
			if i > 0 {
				label = ""
			}
			pct := float64(rule.Failures) / float64(rule.Attempts) * 100
			fmt.Fprintf(&b, "  %-*s%s  %s, %d attempts, %d failed (%.0f%%)\n",
				statusLabelWidth, label, rule.Rule, ruleActionLabel(rule.Source), rule.Attempts, rule.Failures, pct)
		}
		where := r.ConfigPath
		if where == "" {
			where = "your config file"
		}
		// **出口失败要给出口的下一步,不是「改 rules」。**
		//
		// 具名出口失败最常见的原因是那条 `ssh -D` 断了 —— 规则完全正确。
		// 照旧建议改规则,会把人指向删掉一条对的规则,而问题原样留着。
		// (与 doctor 里 direct_egress 那处同一个形状、同一条纪律。)
		if egressFailing(failing) {
			fmt.Fprintf(&b, "  %-*s↑ that egress is unreachable (did the SOCKS5 you had open go away?); bring it back first — to change it, use bx egress\n",
				statusLabelWidth, "")
			for i, w := range r.Warnings {
				label := "Notice"
				if i > 0 {
					label = ""
				}
				fmt.Fprintf(&b, "  %-*s%s\n", statusLabelWidth, label, WarningText(w))
			}
			fmt.Fprint(&b, recoveryHint(r))
			return b.String()
		}
		// 续行与上面的规则名对齐:用同一个标签宽度构造,不手数空格
		// (手数的那个版本差了一格,而这种错没有任何测试会红)。
		fmt.Fprintf(&b, "  %-*s↑ that path is dead; edit the rules in %s, then bx down && bx up\n",
			statusLabelWidth, "", where)
	}

	for i, w := range r.Warnings {
		label := "Notice"
		if i > 0 {
			label = ""
		}
		fmt.Fprintf(&b, "  %-*s%s\n", statusLabelWidth, label, WarningText(w))
	}
	fmt.Fprint(&b, recoveryHint(r))
	return b.String()
}

// WarningText composes a Warning's displayable line: Detail (or Name as
// fallback when Detail is empty) plus, if present, the actionable Hint.
// Exported so cli's up-summary and stats' status panel share one
// composition instead of each growing its own (this repo's recurring
// defect shape: one safety-adjacent thing, two implementations, only one
// remembered).
func WarningText(w Warning) string {
	detail := strings.TrimSpace(w.Detail)
	if detail == "" {
		detail = w.Name
	}
	if w.Hint != "" {
		return detail + " (" + w.Hint + ")"
	}
	return detail
}

// recoveryHint:隧道不健康时返回大白话恢复块(怎么了 + kill-switch 保护说明 + 下一步);
// 健康返回 ""(面板不加噪音)。纯函数,人面专用。
func recoveryHint(r Report) string {
	if r.TunnelHealthy {
		return ""
	}
	return fmt.Sprintf(`
  ⚠ The tunnel is unhealthy: the server may be blocked, or the network is flapping.
    Your real IP is still covered by the kill-switch (losing the outside world
    for a moment is the protection working, not a failure).
    Things to try:
      · wait ten or twenty seconds and see if it reconnects itself (%d reconnects so far)
      · bx doctor                to look for a cause
      · switch to a harder-to-block transport (brook→REALITY), or `+elevate.Prefix+`bx setup with a new link
`, r.Restarts)
}

// RenderNotRunning:bx status 连不上守护进程时的人面提示(daemon 未起)。
func RenderNotRunning() string {
	// elevate.Note() 在有 sudo 的平台上是空串,于是这一行逐字不变;
	// Windows 上补一句「要在管理员 PowerShell 里跑」—— 裸命令自己说不出它需要提权。
	return "bx is not running.\n  Start it: " + elevate.Cmd("bx up") + elevate.Note() + "        Check it: bx doctor\n"
}

// transportWithoutRedundantHost 把 `reality@<host>` 里那个**与 Server 行相同**的
// 地址省掉,只留传输名。
//
// 真机上那一屏是这样的(2026-09-18):
//
//	Server  203.0.113.92  (socks 127.0.0.1:64213)
//	Via     reality@203.0.113.92  UDP→hysteria2@203.0.113.92
//
// 同一个地址在相邻两行里印了三遍,一个新信息都没带。
//
// **地址不同的时候必须留着** —— `udp.transport` 指向另一台服务器是 bx 支持的真实
// 配置,那时这两个 host 的差别恰恰是这一行最值钱的信息。所以判据是「与 Server
// 相同才省」,不是无脑去掉 `@` 后面的东西。
func transportWithoutRedundantHost(transport, server string) string {
	if server == "" {
		return transport
	}
	if name, host, ok := strings.Cut(transport, "@"); ok && host == server {
		return name
	}
	return transport
}

// HumanBytes 把字节数转成人类可读单位。
//
// 导出是因为 internal/cli 的下载进度要用同一份换算:两处各写一份,同一个数会在
// `bx status` 与更新进度里长得不一样,而用户会以为那是两件事。
func HumanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// ruleActionLabel 说明这条规则把连接**逼去了哪**,用户据此判断改它的后果。
func ruleActionLabel(source string) string {
	switch source {
	case "user_direct", "user_direct_ip":
		return "forced direct"
	case "user_proxy", "user_proxy_ip":
		return "forced through the tunnel"
	case "user_egress":
		return "handed to an egress"
	default:
		return "matched"
	}
}

// egressFailing 判断这批失败里**全是**具名出口。
//
// 「全是」而不是「有」:两类混在一起时,改规则那句指引对另一半仍然成立,
// 而漏掉它会让用户找不到真正该改的那一行。
func egressFailing(failing []RuleOutcome) bool {
	if len(failing) == 0 {
		return false
	}
	for _, r := range failing {
		if r.Source != "user_egress" {
			return false
		}
	}
	return true
}

// RuleHistorySnapshot 是跨重启累计的按规则计数,经控制 socket 发布。
//
// 它是**发布形状**,不是盘上那份的镜像:盘上还记着 TrackingLimit 这类只有写入侧
// 用得着的东西,而消费方要的是「累计了多久、多少次判定、跨了几个版本、可不可信」。
type RuleHistorySnapshot struct {
	Rules []RuleOutcome `json:"rules,omitempty"`
	// UptimeSeconds 是 Core **累计在跑**的时长,不是「距首次见到这条规则过了多久」。
	UptimeSeconds int64 `json:"uptime_seconds"`
	// Decisions 是全局累计判定数,**含内建列表命中**。
	Decisions int64 `json:"decisions"`
	// Versions 是这段累积跨过的版本(去重有序),让读的人对累计打折。
	Versions []string `json:"versions,omitempty"`
	// Overflowed 为真时,「某条规则没有条目」不再等于「它没命中过」——
	// 判据据此把死规则整类标为「没查」,而不是把它们全判死。
	//
	// **刻意无 omitempty**:false 与「这一版没有这个字段」必须分得开。
	Overflowed bool      `json:"overflowed"`
	UpdatedAt  time.Time `json:"updated_at"`
	// SkipReason 非空表示这份历史此刻不可用(读不出、schema 不认)。
	// 有它就别拿上面的数字下判断 —— 那几个数是重新开始累计之后的,不是全部历史。
	SkipReason string `json:"skip_reason,omitempty"`
}
