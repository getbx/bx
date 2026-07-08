package stats

import (
	"fmt"
	"strings"
)

// Report 是 bx status 的线材格式:计数快照 + 隧道信息。
type Report struct {
	Snapshot
	Server        string `json:"server"`
	SocksAddr     string `json:"socks_addr"`
	TunnelHealthy bool   `json:"tunnel_healthy"`
	LatencyMS     int64  `json:"latency_ms"`
	Restarts      int    `json:"restarts"`
	Mode          string `json:"mode,omitempty"` // 分流模式:split | global | router
	UDPMode       string `json:"udp_mode"`
	UDPNote       string `json:"udp_note,omitempty"`
	MutationState string `json:"mutation_state,omitempty"`

	Transport    string   `json:"transport,omitempty"`     // 当前活跃传输 scheme@host(容灾后反映实际)
	Transports   []string `json:"transports,omitempty"`    // 多传输容灾列表(>1 时,有序优先级)
	UDPTransport string   `json:"udp_transport,omitempty"` // UDP 专用传输(按类分流)
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
	fmt.Fprintf(&b, "  分流    代理 %.1f%% / 直连 %.1f%%\n", ratio, 100-ratio)
	fmt.Fprintf(&b, "  流量    ↑ %s   ↓ %s\n", humanBytes(r.BytesUp), humanBytes(r.BytesDown))
	fmt.Fprint(&b, recoveryHint(r))
	return b.String()
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
