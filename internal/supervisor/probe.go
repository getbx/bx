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
	// Error 是给人看的失败原因(超时 / 拒绝 / 解析不出主机)。**它是中文的**,
	// 因为它的第一个消费方是 `bx server list`,而 CLI 通篇中文。
	Error string `json:"error,omitempty"`
	// ErrorCode 是同一件事的**机器可读**形式,与 Error 成对出现。
	//
	// **它存在的理由是菜单。** 上面那句中文会一路流到 macOS 菜单里去
	// (probe → Guardian 的 ProbeReport → 服务器窗口那一行),而那个界面通篇英文、
	// 还有一条 CJK 守卫 —— 但守卫只扫 `apps/macos/BxMenu/Sources`,扫不到从服务端
	// 来的字符串,所以「服务器关着」这条**最常见**的失败路径此前会在全英文菜单里
	// 显示一句中文。修法不是把这里翻成英文(那会把 `bx server list` 变成半英文),
	// 是让客户端按码自己出话:**服务端发码,客户端出语言**。
	//
	// 带 omitempty 与 Error 同步:两者都缺席 = 这次没有失败可报。
	ErrorCode string `json:"error_code,omitempty"`
}

// 探测失败的**机器可读**原因码。取值集合就在下面那个数组里,跨语言守卫
// (`internal/cli` 的 TestProbeErrorCodesAllHaveAnEnglishSentenceInTheMenu)
// 拿它与 ServersModel.swift 里那张英文表**双向**对账 —— 少一边就会在界面上
// 静默退化成一句笼统的兜底,而那正是这一条要消灭的失效。
const (
	ProbeErrTimeout            = "timeout"
	ProbeErrCanceled           = "canceled"
	ProbeErrDNS                = "dns"
	ProbeErrRefused            = "refused"
	ProbeErrNetworkUnreachable = "network_unreachable"
	ProbeErrNoRoute            = "no_route"
	ProbeErrNoHost             = "no_host"
	ProbeErrBadPort            = "bad_port"
	ProbeErrUnknown            = "unknown"
	// 下面两个的产地在 Guardian(探测这一步压根没做成),不在本文件 ——
	// 但清单只许有一份,所以它们也登记在这里(与 internal/udpsource、
	// internal/barriercidr 同一条:让漂移在构造上不可能)。
	ProbeErrCoreUnreachable = "core_unreachable"
	ProbeErrLinkUnparsed    = "link_unparsed"
)

// ProbeErrorCodes 是上面全部取值。**新增一个码必须同时登记在这里**,
// 否则跨语言守卫看不见它,而界面会静默退回兜底文案。
var ProbeErrorCodes = []string{
	ProbeErrTimeout, ProbeErrCanceled, ProbeErrDNS, ProbeErrRefused,
	ProbeErrNetworkUnreachable, ProbeErrNoRoute, ProbeErrNoHost,
	ProbeErrBadPort, ProbeErrUnknown,
	ProbeErrCoreUnreachable, ProbeErrLinkUnparsed,
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
		result.Error, result.ErrorCode = "there is no host to test", ProbeErrNoHost
		return result
	}
	if port > 65535 {
		result.Error, result.ErrorCode = fmt.Sprintf("the port is not valid: %d", port), ProbeErrBadPort
		return result
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	start := time.Now()
	conn, err := dial.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	elapsed := time.Since(start)
	if err != nil {
		result.ErrorCode = classifyProbeError(err)
		result.Error = ProbeErrorText(result.ErrorCode)
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

// classifyProbeError 把拨号错误归到一个机器可读的码上。
//
// **原始错误不外传**:它里面有本机接口名、路由细节这类实现内部的东西,而用户
// 需要的只是「关着 / 太慢 / 域名解析不出来」这三类里的哪一类。
func classifyProbeError(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return ProbeErrTimeout
	case errors.Is(err, context.Canceled):
		return ProbeErrCanceled
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return ProbeErrDNS
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		if opErr.Timeout() {
			return ProbeErrTimeout
		}
		var syscallMsg string
		if opErr.Err != nil {
			syscallMsg = opErr.Err.Error()
		}
		switch {
		case strings.Contains(syscallMsg, "connection refused"):
			return ProbeErrRefused
		case strings.Contains(syscallMsg, "network is unreachable"):
			return ProbeErrNetworkUnreachable
		case strings.Contains(syscallMsg, "no route to host"):
			return ProbeErrNoRoute
		}
	}
	return ProbeErrUnknown
}

// ProbeErrorText 是那些码的**中文**说法,给 CLI 用(`bx server list`)。
//
// **导出是因为 Guardian 那两处产地也要用它。** 探测这一步压根没做成时
// (Core 拨不通 / 链接里解不出主机)报告是 Guardian 自己拼的;它一度在那儿写
// 英文,理由是「那句话会出现在全英文菜单里」—— 而菜单如今根本不读这个字段
// (它读码、自己出英文)。于是那两句英文只剩一个消费方:**中文的 CLI**。
// 让两处产地都经这一份,既把语言拨回来,也让「码 ↔ 中文」这张表在生产里
// 真的**每一条都可达** —— 否则守卫声称覆盖 11 个码,实际只覆盖得到 9 个。
//
// 认不出的码退回「连不上」——一个码走丢了应当读起来像一次普通的失败,
// 而不是一个协议串。
func ProbeErrorText(code string) string {
	switch code {
	case ProbeErrTimeout:
		return "timed out (no answer)"
	case ProbeErrCanceled:
		return "canceled"
	case ProbeErrDNS:
		return "the domain does not resolve"
	case ProbeErrRefused:
		return "connection refused (nothing is listening on that port)"
	case ProbeErrNetworkUnreachable:
		return "the network is unreachable"
	case ProbeErrNoRoute:
		return "there is no route to that host"
	case ProbeErrNoHost:
		return "there is no host to test"
	case ProbeErrBadPort:
		return "the port is not valid"
	case ProbeErrCoreUnreachable:
		return "could not measure (is bx running?)"
	case ProbeErrLinkUnparsed:
		return "no host could be parsed out of the link"
	}
	return "unreachable"
}
