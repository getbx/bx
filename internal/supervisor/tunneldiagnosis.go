package supervisor

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"syscall"
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

	// ErrTunnelUndeterminedUDPTransport:这一种传输不在 TCP 上听,一次拨号
	// 观测不到那台服务器。**不是「没应答」,也不是「我们没问」。**
	ErrTunnelUndeterminedUDPTransport = errors.New("这一种传输不在 TCP 上听,一次 TCP 拨号观测不到那台服务器")
	// ErrTunnelUndeterminedLocalDial:判别那次拨号**在本机就失败了**,SYN 没
	// 能离开这台机器 —— 要查的是 bx 自己的直连器,不是那台服务器。
	ErrTunnelUndeterminedLocalDial = errors.New("判别拨号在本机就失败了,SYN 没能离开这台机器")
)

const (
	StartFailureTunnelUnreachable     = "tunnel_unreachable"
	StartFailureTunnelHandshakeFailed = "tunnel_handshake_failed"

	// 「没判出来」有**五种来由**,而其中两种的处置与其余三种完全不同 ——
	// 所以它们各有自己的码。仍然只有码,发布面一寸没扩(spec §5)。
	//
	//   - UDP 传输:那台服务器根本不在 TCP 上听(hysteria2 是 QUIC)。要告诉
	//     用户的是「bx 这次观测对这种传输不成立」—— 既不是「服务器没应答」,
	//     也不是「我们没去问」;想确认那台机器活着得换个手段(ping/ssh)。
	//   - 本机拨号失败:SYN 根本没离开这台机器。**它指着 bx 自己的直连器,
	//     不指着 VPS** —— 2026-08-13 那次真机事故的签名(DirectDialer 用
	//     IP_BOUND_IF 绑物理网卡,而那条 scoped 默认路由由 Hijack 装,比这次
	//     判别拨号晚 572 行)。给用户的下一步是 `route -n get -ifscope <网卡>`,
	//     去查 VPS 是白费力气。
	//
	// 其余三种(解不出 host:port / DNS 解析不了 / 父 ctx 被取消)共用笼统那个码:
	// 它们的下一步是同一句「看 Core 日志、自己确认一下那台服务器」。
	//
	// **三个码共享同一个前缀,而且是由拼接得来的**(不是三个各写一遍的字面量):
	// 任何消费方都不可能把其中之一读成「那台服务器没事」,而将来加第四种时
	// 它自动进这一族。判据是 IsTunnelUndeterminedCode。
	StartFailureTunnelUndeterminedUDPTransport = StartFailureTunnelUndetermined + "_udp_transport"
	StartFailureTunnelUndeterminedLocalDial    = StartFailureTunnelUndetermined + "_local_dial"
)

// IsTunnelUndeterminedCode:这个码是不是「隧道没起来,而 bx 没能判断那台
// 服务器还在不在」那一族。
//
// 消费方(CLI 与菜单)拿它兜底:漏掉一种具体来由时落回笼统那句话,而**绝不会**
// 落进「服务器活着 / 服务器挂了」两个具体答案里的任何一个。
func IsTunnelUndeterminedCode(code string) bool {
	return strings.HasPrefix(code, StartFailureTunnelUndetermined)
}

// TunnelDiagnosisTimeout 是判别那一次拨号的上限。它只在启动已经失败之后发生,
// 而用户此刻正站在那儿等一句话 —— 5 秒是「够判出来」与「别再让他多等」之间的取舍。
//
// **导出是因为 Guardian 要靠它算自己的等待预算**:Core 直到这次判别做完才写下
// 那份启动失败记录,而 Guardian 的健康等待与 Core 的隧道健康窗口是同一个 20 秒
// (两边各自的默认值)—— 也就是说 Guardian 放弃的那一刻,Core 还差整整一次判别
// 才会开口。Guardian 那边的余量因此从这个数派生,不许手抄一个秒数
// (见 guardian/corestartfailure.go 的 coreStartFailureGrace)。
const TunnelDiagnosisTimeout = 5 * time.Second

// transportsAnsweringTCP 说明:对这一种传输的 host:port 拨一次 TCP,到底观测
// 得到什么。
//
// **hysteria2 跑在 QUIC/UDP 上** —— 一台活得好好的 hysteria2 服务器不会应答
// TCP SYN,而 `bx server deploy` 把 443/tcp 也放行了,于是那次拨号连「被拒绝」
// 都不是,一路超时 ⇒ 判成「那台机器可能挂了、或者换了 IP」,而它正常得很。
// 它不是边角:transportKind 认它、`bx server install --protocol hysteria2` 装它、
// CLAUDE.md 记着六种传输真机 e2e 全过。
//
// **判据取传输种类,不取端口号** —— 端口号说不出上面在跑什么协议。
//
// 认不出的种类查出 false(=判不出来):将来加一种传输而忘了登记时,落回的是
// 诚实答案,不是一个可能错的具体答案。与「ErrTunnelUnhealthy 自己就是
// undetermined 那一档」同一条极性。
//
// **键取 tunnel 包的常量、穷举由 tunnel.Kinds() 驱动**:上一版这里是六个手抄的
// 字符串,而守着它的那条测试自称「覆盖每一种 transportKind 会返回的传输」,
// 比对的却是它自己手抄的第二张表 —— 给 tunnel.Kind 加一种 tuic,两个包全绿,
// 那句「穷举」变成假话。清单下沉到 internal/tunnel 之后漂移在构造上不可能。
var transportsAnsweringTCP = map[string]bool{
	tunnel.KindReality:     true,
	tunnel.KindTrojan:      true,
	tunnel.KindShadowsocks: true,
	tunnel.KindVmess:       true,
	// brook server 在同一个端口上同时听 TCP 与 UDP。
	tunnel.KindBrook: true,
	// QUIC/UDP:TCP 那一侧没有任何东西在听。
	tunnel.KindHysteria2: false,
}

// posixDialFailuresBeforeTheSYNLeaves 是**本机自己**没能把 SYN 发出去的那些形状。
// 它们与 *net.DNSError 同一族:连问都没问到那台服务器,所以只能判「没判出来」。
//
// **被拒绝与超时不在这张表里**(台账 ruling ②):那两种是那台服务器给的答复,
// 2026-09-12 事故的原文正是 i/o timeout,把它们判成「问不出来」等于把这一支
// 要给用户的那个答案整个扔掉。
var posixDialFailuresBeforeTheSYNLeaves = []error{
	syscall.ENETUNREACH,   // 没有到那个目的地的路由(scoped 表是空的)
	syscall.EHOSTUNREACH,  // 有路由,但本机就判定这台主机够不着
	syscall.EACCES,        // 本机策略挡下了这次出站(沙盒 / 防火墙)
	syscall.EADDRNOTAVAIL, // 绑不上源地址 —— 也在离开本机之前
}

// dialFailuresBeforeTheSYNLeaves = 上面那些 + 本平台的孪生值。
//
// **Windows 上那四个 syscall 常量是编造出来的**:zerrors_windows.go 把它们定义成
// `APPLICATION_ERROR + iota`(≥ 2^29),winsock 一次都不会返回;真实的那次失败是
// `WSAENETUNREACH`(10051),而 `syscall.Errno.Is` 在两者之间**什么都不映射**
// (它只桥接 ErrPermission/ErrExist/ErrNotExist/ErrUnsupported 四个 oserror)。
// 也就是说这张表在 Windows 上是**死的** —— 而 Windows 真的会跑 supervisor.Run
// (服务),它的 DirectDialer 也真的绑物理网卡(IP_UNICAST_IF),于是同一个
// 「本机自己不可达」照样被报成「你的 VPS 挂了」。同一个缺陷活在第三个平台上。
//
// darwin/linux 上 platformDialFailuresBeforeTheSYNLeaves 是 nil,行为逐字节不变。
var dialFailuresBeforeTheSYNLeaves = append(
	append([]error(nil), posixDialFailuresBeforeTheSYNLeaves...),
	platformDialFailuresBeforeTheSYNLeaves...,
)

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
// 链接里那个 host:port 直连拨一次 TCP —— **除非那一种传输根本不在 TCP 上听**
// (hysteria2 是 QUIC/UDP),那时一次拨号什么也观测不到,直接落「没判出来」
// 而不拨(transportsAnsweringTCP)。
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
	kind := transportKind(link)
	if !transportsAnsweringTCP[kind] {
		// 这一种传输的服务器**不在 TCP 上听**(hysteria2 是 QUIC/UDP)。拨过去
		// 拿到的既不是「它挂了」也不是「它活着」,只是「TCP 那边没人」——
		// 而 §4.5 最长的那一段禁止的正是拿这种非观测去下一个具体结论。
		return tunnelUndetermined(ErrTunnelUndeterminedUDPTransport, cause,
			fmt.Sprintf("%s 跑在 UDP 上,一次 TCP 拨号观测不到那台服务器", kind))
	}
	addr, err := serverDialAddress(link)
	if err != nil {
		// **不带上那个错误的文本**:url.Parse 失败时 *url.Error 会原样打印整条
		// 链接,vless 的 UUID 就在里面。这句话进 Core 日志,而这一族的码还要
		// 往 Guardian 送 —— 一条凭据一旦进了会被转发的字符串就再也收不回来。
		return tunnelUndetermined(ErrTunnelUnhealthy, cause, "没能从服务器链接里解出 host:port")
	}
	if dialer == nil {
		return tunnelUndetermined(ErrTunnelUnhealthy, cause, "没有可用的判别拨号器")
	}
	dial := dialer()
	if dial == nil {
		return tunnelUndetermined(ErrTunnelUnhealthy, cause, "没有可用的判别拨号器")
	}
	dialCtx, cancel := context.WithTimeout(ctx, TunnelDiagnosisTimeout)
	defer cancel()
	conn, dialErr := dial(dialCtx, "tcp", addr)
	if dialErr == nil {
		_ = conn.Close()
		return tagStartFailure(ErrTunnelHandshakeFailed,
			fmt.Errorf("服务器 %s 的 TCP 端口在应答: %w", addr, cause))
	}
	// 父 ctx 挂了 ⇒ **是我们自己没问完**,不是那台服务器没答。
	if ctx.Err() != nil {
		return tunnelUndetermined(ErrTunnelUnhealthy, cause, fmt.Sprintf("判别过程本身被打断:%v", ctx.Err()))
	}
	// 名字都没解出来 ⇒ 连问都没问到 TCP 那一层。说「端口没有应答」是在替一个
	// 我们根本没做过的观测下结论。
	var dnsErr *net.DNSError
	if errors.As(dialErr, &dnsErr) {
		return tunnelUndetermined(ErrTunnelUnhealthy, cause, fmt.Sprintf("服务器域名没能解析:%v", dialErr))
	}
	// 同一族的另一半:**SYN 根本没能离开本机**。
	//
	// ENETUNREACH 有真机先例 —— DirectDialer 用 IP_BOUND_IF 绑物理网卡,而
	// IP_BOUND_IF 只查 **scoped** 路由表;那条 scoped 默认路由是 Hijack
	// (run.go:880)装的,判别拨号在 run.go:308,**早 572 行**。一台 macOS 自己
	// 没建 per-interface default 的机器上(2026-08-13 那次事故的形状),每一次
	// 判别拨号都在本地就 network is unreachable,于是不管 VPS 在做什么,bx 都
	// 答「你的 VPS 挂了」—— 而这条路唯一的职责就是说出关于那台服务器的实话。
	for _, localFailure := range dialFailuresBeforeTheSYNLeaves {
		if errors.Is(dialErr, localFailure) {
			return tunnelUndetermined(ErrTunnelUndeterminedLocalDial, cause,
				fmt.Sprintf("这次拨号在本机就失败了(%v)", dialErr))
		}
	}
	// 拨不通(拒绝、超时、不可达)—— **超时归这一档,不归「没判出来」**:
	// 2026-09-12 那次事故的原文正是 `i/o timeout`,一台不通的 VPS 给出的就是
	// 这个形状,把它判成「问不出来」等于把最常见的那一种答案扔掉。
	return tagStartFailure(ErrTunnelUnreachable,
		fmt.Errorf("到服务器 %s 的 TCP 连接没有建立(%v): %w", addr, dialErr, cause))
}

// tunnelUndetermined:隧道没起来,而 bx **没能判断**那台服务器还在不在。
// 两条动作都要给,一条都不许断言 —— 与 observe.Tristate 同一条纪律。
//
// sentinel 选的是这一族里对得上的那一档:笼统那个是家长 ErrTunnelUnhealthy,
// 另外两档各有自己的哨兵(它们的处置与笼统那三种完全不同,见上面那段常量)。
// **无论选哪一个,家长都还在错误链上** —— cause 本身就是 waitTunnelHealthy 挂了
// ErrTunnelUnhealthy 的那个错误,于是别处按 errors.Is(err, ErrTunnelUnhealthy)
// 做的判定一个字不用改。
func tunnelUndetermined(sentinel, cause error, why string) error {
	return tagStartFailure(sentinel,
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
