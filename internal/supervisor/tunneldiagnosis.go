package supervisor

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/getbx/bx/internal/tunnel"
)

// 「隧道没起来」必须一分为二 —— 两种故障的处置完全相反。
//
//   - **服务器的 TCP 端口根本连不上** ⇒ 那台机器挂了 / 端口被挡 / 换了 IP。
//     动作是换一台或者去修 VPS。
//   - **TCP 连得上,而隧道就是不健康** ⇒ 服务器活着,问题在链接、凭据、SNI、
//     或路上的干扰。动作完全不同。
//
// 本仓库为第二种付过一次大代价:reality 一度全挂,真因是默认 SNI
// www.microsoft.com 的证书过大,而当时先误归因成 sing-box 同机问题、又误归因
// 成网络 MITM(CLAUDE.md「reality 传输收尾」教训坑 ①)。**把这两种压成一个码,
// 等于把那次教训重新埋回去。**
var (
	// ErrTunnelUnreachable:那台服务器的 TCP 端口没有应答。
	ErrTunnelUnreachable = errors.New("连不上服务器,那个端口没有应答")
	// ErrTunnelHandshakeFailed:TCP 连得上,而隧道没能建起来。
	ErrTunnelHandshakeFailed = errors.New("服务器在应答,但隧道没能建起来")
)

const (
	StartFailureTunnelUnreachable     = "tunnel_unreachable"
	StartFailureTunnelHandshakeFailed = "tunnel_handshake_failed"
)

// tunnelDiagnosisTimeout 是判别那一次拨号的上限。它只在启动已经失败之后发生,
// 而用户此刻正站在那儿等一句话 —— 5 秒是「够判出来」与「别再让他多等」之间的取舍。
const tunnelDiagnosisTimeout = 5 * time.Second

type tunnelDialFunc func(ctx context.Context, network, address string) (net.Conn, error)

// tunnelDiagnosisDialer 交出判别用的直连拨号器。
//
// **做成 thunk 而不是直接传一个拨号器**:darwin 的 DirectDialer() 每次都要去
// 问一遍默认路由(exec 一个 route 进程),而判别只发生在失败路径上 —— 成功
// 启动的那条路上连造都不该造它。这条与所有者定死的边界「不后台定时探测」
// 是同一件事:这次拨号是一次一次性的、由失败触发的观测,不是一个探针。
type tunnelDiagnosisDialer func() tunnelDialFunc

// awaitTunnelHealthOrDiagnose 等隧道健康;不健康就**当场判别一次**,把
// 「隧道没起来」拆成三档里对得上的那一档。
//
// 判别**不读 sing-box 的 stderr 文本**(判据不许长在文本匹配上,而且那份日志
// 是多次 spawn 共用的,分不清哪几行属于这一次),而是去做一次观测:对服务器
// 链接里那个 host:port 直连拨一次。
//
// 这次拨号不新增任何暴露面:目的地是用户自己的服务器,而 bx 刚刚已经朝它连拨
// 了 20 秒;此刻还没有 Hijack(它排在更后面),普通 socket 走的就是物理网卡。
func awaitTunnelHealthOrDiagnose(ctx context.Context, t *tunnel.Tunnel, timeout time.Duration, link string, dialer tunnelDiagnosisDialer) error {
	err := waitTunnelHealthy(ctx, t, timeout)
	if err == nil {
		return nil
	}
	if !errors.Is(err, ErrTunnelUnhealthy) {
		// ctx 被取消(用户关机 / 死手到点)不是启动失败,更不该为它去拨一次号。
		return err
	}
	return diagnoseUnhealthyTunnel(ctx, link, dialer, err)
}

func diagnoseUnhealthyTunnel(ctx context.Context, link string, dialer tunnelDiagnosisDialer, cause error) error {
	addr, err := serverDialAddress(link)
	if err != nil {
		return tunnelUndetermined(cause, fmt.Sprintf("没能从服务器链接里解出 host:port:%v", err))
	}
	if dialer == nil {
		return tunnelUndetermined(cause, "没有可用的判别拨号器")
	}
	dial := dialer()
	if dial == nil {
		return tunnelUndetermined(cause, "没有可用的判别拨号器")
	}
	dialCtx, cancel := context.WithTimeout(ctx, tunnelDiagnosisTimeout)
	defer cancel()
	conn, dialErr := dial(dialCtx, "tcp", addr)
	if dialErr == nil {
		_ = conn.Close()
		return tagStartFailure(ErrTunnelHandshakeFailed,
			fmt.Errorf("服务器 %s 的 TCP 端口在应答: %w", addr, cause))
	}
	// 父 ctx 挂了 ⇒ **是我们自己没问完**,不是那台服务器没答。
	if ctx.Err() != nil {
		return tunnelUndetermined(cause, fmt.Sprintf("判别过程本身被打断:%v", ctx.Err()))
	}
	// 名字都没解出来 ⇒ 连问都没问到 TCP 那一层。说「端口没有应答」是在替一个
	// 我们根本没做过的观测下结论。
	var dnsErr *net.DNSError
	if errors.As(dialErr, &dnsErr) {
		return tunnelUndetermined(cause, fmt.Sprintf("服务器域名没能解析:%v", dialErr))
	}
	// 拨不通(拒绝、超时、不可达)—— **超时归这一档,不归「没判出来」**:
	// 2026-09-12 那次事故的原文正是 `i/o timeout`,一台不通的 VPS 给出的就是
	// 这个形状,把它判成「问不出来」等于把最常见的那一种答案扔掉。
	return tagStartFailure(ErrTunnelUnreachable,
		fmt.Errorf("到服务器 %s 的 TCP 连接没有建立(%v): %w", addr, dialErr, cause))
}

// tunnelUndetermined:隧道没起来,而 bx **没能判断**那台服务器还在不在。
// 两条动作都要给,一条都不许断言 —— 与 observe.Tristate 同一条纪律。
func tunnelUndetermined(cause error, why string) error {
	return tagStartFailure(ErrTunnelUnhealthy,
		fmt.Errorf("没能判断服务器还在不在(%s): %w", why, cause))
}

// serverDialAddress 从服务器链接解出 host:port。
//
// 端口走 tunnel.ServerPort 而不是在这里 url.Parse 一下 —— ss/vmess 的 authority
// 是 base64、brook 的地址在查询参数里,随手解出来的端口对这三种全是 0,而那个
// 0 会让一台跑在 5443 上的健康服务器被测成不可达。
func serverDialAddress(link string) (string, error) {
	host, err := serverHostFromLink(link)
	if err != nil {
		return "", err
	}
	port, err := tunnel.ServerPort(link)
	if err != nil {
		return "", err
	}
	return net.JoinHostPort(host, strconv.Itoa(port)), nil
}
