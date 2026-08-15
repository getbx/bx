package stats

import (
	"fmt"
	"strings"
	"time"
)

// Report 是 bx status 的线材格式:计数快照 + 隧道信息。
// Guardian 的非秘密交接状态由 supervisor.RuntimeState 单独承载,避免改变 /v0/status 契约。
type Report struct {
	Snapshot
	Server        string `json:"server"`
	SocksAddr     string `json:"socks_addr"`
	TunnelHealthy bool   `json:"tunnel_healthy"`
	LatencyMS     int64  `json:"latency_ms"`
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

// modeLabel 给分流模式配中文说明,让 status 一眼看懂当前流量策略。
func modeLabel(mode string) string {
	switch mode {
	case "global":
		return "global(含国内全走隧道)"
	case "router":
		return "router(只劫持 LAN 转发)"
	case "router-global":
		return "router · 白名单(LAN 转发全走隧道,仅白名单直连)"
	case "split":
		return "split(国内直连 / 境外走隧道)"
	default:
		return mode
	}
}

// Render 把 Report 渲染成命令行状态面板。
func Render(r Report) string {
	health := "● 健康"
	if !r.TunnelHealthy {
		health = "○ 不健康"
	}
	ratio := r.ProxyRatio() * 100
	var b strings.Builder
	fmt.Fprintln(&b, "bx 状态")
	fmt.Fprintf(&b, "  节点    %s  (socks %s)\n", r.Server, r.SocksAddr)
	fmt.Fprintf(&b, "  隧道    %s  延迟 %dms  重连 %d\n", health, r.LatencyMS, r.Restarts)
	if r.Mode != "" {
		fmt.Fprintf(&b, "  模式    %s\n", modeLabel(r.Mode))
	}
	if r.Transport != "" {
		fmt.Fprintf(&b, "  传输    %s", r.Transport)
		if len(r.Transports) > 1 {
			fmt.Fprintf(&b, "  (容灾 %s)", strings.Join(r.Transports, " › "))
		}
		if r.UDPTransport != "" {
			fmt.Fprintf(&b, "  UDP→%s", r.UDPTransport)
		}
		fmt.Fprintln(&b)
	}
	fmt.Fprintf(&b, "  连接    活跃 %d  代理 %d  直连 %d  阻断 %d\n", r.Active, r.Proxy, r.Direct, r.Blocked)
	udpMode := r.UDPMode
	if udpMode == "" {
		udpMode = "proxy"
	}
	fmt.Fprintf(&b, "  UDP     mode %s  阻断 %d", udpMode, r.UDPBlocked)
	if r.UDPNote != "" {
		fmt.Fprintf(&b, "  %s", r.UDPNote)
	}
	fmt.Fprintln(&b)
	// **失败与判定分开报,而且只在真有失败时才占一行。**
	// bx 就在数据面上,这些失败它每一次都看见 —— 此前只打进 debug 日志然后扔掉,
	// 于是「direct 26186」里藏着几千条秒失败的连接,而 status 一个字都不说。
	if r.DirectFailed > 0 || r.ProxyFailed > 0 {
		fmt.Fprintf(&b, "  失败    代理 %d  直连 %d\n", r.ProxyFailed, r.DirectFailed)
	}
	fmt.Fprintf(&b, "  分流    代理 %.1f%% / 直连 %.1f%%\n", ratio, 100-ratio)
	fmt.Fprintf(&b, "  流量    ↑ %s   ↓ %s\n", humanBytes(r.BytesUp), humanBytes(r.BytesDown))
	// 点名成片失败的用户规则。**一切正常时这里一个字都不打** ——
	// 那是它不被训练成噪声的前提。
	if failing := r.FailingRules(); len(failing) > 0 {
		for i, rule := range failing {
			label := "规则"
			if i > 0 {
				label = ""
			}
			pct := float64(rule.Failures) / float64(rule.Attempts) * 100
			fmt.Fprintf(&b, "  %-6s%s  %s %d 条,失败 %d(%.0f%%)\n",
				label, rule.Rule, ruleActionLabel(rule.Source), rule.Attempts, rule.Failures, pct)
		}
		where := r.ConfigPath
		if where == "" {
			where = "配置文件"
		}
		// 续行与上面的规则名对齐:用同一个标签宽度构造,不手数空格
		// (手数的那个版本差了一格,而这种错没有任何测试会红)。
		fmt.Fprintf(&b, "  %-6s↑ 这条路已经不通;改 %s 的 rules 后 bx down && bx up\n", "", where)
	}

	for i, w := range r.Warnings {
		label := "提醒"
		if i > 0 {
			label = ""
		}
		fmt.Fprintf(&b, "  %-6s %s\n", label, warningText(w))
	}
	fmt.Fprint(&b, recoveryHint(r))
	return b.String()
}

func warningText(w Warning) string {
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
  ⚠ 隧道不健康:可能是服务器被封或网络波动。
    你的真实 IP 已被 kill-switch 保护(外网暂时不通是「保护」,不是故障)。
    可以试:
      · 稍等十几秒看是否自动重连(已重连 %d 次)
      · bx doctor                体检找原因
      · 让你的 agent 换隐写传输(brook→REALITY)绕过封锁,或 sudo bx setup 换新链接
`, r.Restarts)
}

// RenderNotRunning:bx status 连不上守护进程时的人面提示(daemon 未起)。
func RenderNotRunning() string {
	return "bx 未运行。\n  启动:sudo bx up        体检:bx doctor\n"
}

// humanBytes 把字节数转成人类可读单位。
func humanBytes(n int64) string {
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
		return "强制直连"
	case "user_proxy", "user_proxy_ip":
		return "强制走隧道"
	default:
		return "命中"
	}
}
