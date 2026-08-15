package supervisor

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// 「那台服务器现在通不通、多远」。
//
// ## 为什么这件事必须由 Core 做
//
// 判据是**从这台机器直连过去的往返时间**。bx 开着的时候,一个普通 socket 打到
// 另一台服务器上会被 TUN 抓走、经**当前**那条隧道绕出去 —— 量到的是
// 「你 → 当前服务器 → 目标」,那个数字对「该换哪一台」这个问题毫无意义,
// 而且它看起来完全正常。只有 Core 手里那个 DirectDialer(macOS 的 IP_BOUND_IF /
// Linux 的 SO_MARK)绕得开自己装的路由。
//
// ## 它走在隧道外面,所以只在用户点的时候发
//
// 探测会让网络上看得见「这台机器联系过那个 IP」。**绝不做后台定时探测** ——
// 那等于一个隐私工具定期发不受保护的流量(这条在 2026-08-13 已经论证过一次,
// 当时否掉的是菜单栏那行常驻红字)。被动观测优于主动探测;主动探测只在用户
// 明确要一个「现在」的答案时发生。

// probeTimeout 单次探测的上限。
//
// 短是刻意的:用户点了「测一下」是在等结果,而一台**关着的**服务器最常见的
// 表现就是不应答直到超时。8 秒足够跨太平洋的三次握手完成好几趟。
const probeTimeout = 8 * time.Second

// ProbeRequest 是要测的那台。
type ProbeRequest struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

// ProbeResult 是一次探测的结果。
//
// **Reachable 与 RTT 分开。** 把「没通」表达成 RTT=0 会让界面显示「0 毫秒」,
// 那是这个仓库反复禁止的那种谎(零值读起来像一切正常)。
type ProbeResult struct {
	Host      string `json:"host"`
	Port      int    `json:"port"`
	Reachable bool   `json:"reachable"`
	RTTMS     int64  `json:"rtt_ms,omitempty"`
	// Error 是给人看的失败原因(超时 / 拒绝 / 解析不出主机)。
	Error string `json:"error,omitempty"`
}

// probeDialer 是 Core 用来直连的那个拨号器(生产里就是 platform.DirectDialer())。
type probeDialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

// probeServer 量一次 TCP 握手的往返时间。
//
// **判据是握手完成,不是端口开着。** 一台 reality 服务器对未认证的连接会把流量
// 中继到真站去,所以我们既做不到也不需要验证协议;能三次握手就说明这条路是通的、
// 而且这就是延迟。
func probeServer(ctx context.Context, dial probeDialer, req ProbeRequest) ProbeResult {
	host := strings.TrimSpace(req.Host)
	port := req.Port
	if port <= 0 {
		port = 443
	}
	result := ProbeResult{Host: host, Port: port}
	if host == "" {
		result.Error = "没有主机可测"
		return result
	}
	if port > 65535 {
		result.Error = fmt.Sprintf("端口不合法:%d", port)
		return result
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	start := time.Now()
	conn, err := dial.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	elapsed := time.Since(start)
	if err != nil {
		result.Error = describeProbeError(err)
		return result
	}
	_ = conn.Close()
	result.Reachable = true
	// 向上取整到 1ms:同机回环会量出 0,而「0 毫秒」读起来像没量。
	result.RTTMS = int64(elapsed / time.Millisecond)
	if result.RTTMS == 0 {
		result.RTTMS = 1
	}
	return result
}

// describeProbeError 把拨号错误翻成一句用户读得懂的话。
//
// **原始错误不外传**:它里面有本机接口名、路由细节这类实现内部的东西,而用户
// 需要的只是「关着 / 太慢 / 域名解析不出来」这三类里的哪一类。
func describeProbeError(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "超时(没有应答)"
	case errors.Is(err, context.Canceled):
		return "已取消"
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "域名解析不出来"
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		if opErr.Timeout() {
			return "超时(没有应答)"
		}
		var syscallMsg string
		if opErr.Err != nil {
			syscallMsg = opErr.Err.Error()
		}
		switch {
		case strings.Contains(syscallMsg, "connection refused"):
			return "连接被拒(端口没在听)"
		case strings.Contains(syscallMsg, "network is unreachable"):
			return "网络不可达"
		case strings.Contains(syscallMsg, "no route to host"):
			return "没有到该主机的路由"
		}
	}
	return "连不上"
}
