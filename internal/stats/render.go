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
	fmt.Fprintf(&b, "  连接    活跃 %d  代理 %d  直连 %d  阻断 %d\n", r.Active, r.Proxy, r.Direct, r.Blocked)
	fmt.Fprintf(&b, "  分流    代理 %.1f%% / 直连 %.1f%%\n", ratio, 100-ratio)
	fmt.Fprintf(&b, "  流量    ↑ %s   ↓ %s\n", humanBytes(r.BytesUp), humanBytes(r.BytesDown))
	return b.String()
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
