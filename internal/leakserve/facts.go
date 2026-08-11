package leakserve

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"time"

	"github.com/getbx/bx/internal/leakcheck"
	"github.com/getbx/bx/internal/tristate"
)

// ErrNoRoute 表示**确知**没有这条路由(不是查询失败)。
//
// 这两件事必须分得开:「确知没有 v6 默认路由」能支撑一句诚实的 ok(这台机器
// 没有 v6 通路可漏),而「v6 查询失败」什么都支撑不了。把后者当成前者,就是
// 让 bx 对一台可能正在漏 v6 的机器说「没问题」。
var ErrNoRoute = errors.New("no route")

// probeV4 / probeV6 是用来问「默认路由归谁」的目的地。选公共 anycast 解析器
// 是因为它们必定落在默认路由上,不在任何私网/carve-out 网段里。
const (
	probeV4 = "1.1.1.1"
	probeV6 = "2606:4700:4700::1111"
)

// FactDeps 是采集本机事实所需的外部能力。**用注入而不是直接调**,是为了让
// 采集逻辑在任何平台上都能测(测试里永远不跑 route / networksetup / scutil)。
type FactDeps struct {
	// LookupRoute 回答「发往 dest 的包交给哪个接口」。确知无路由时返回 ErrNoRoute。
	LookupRoute func(ctx context.Context, dest string, ipv6 bool) (string, error)
	// InspectDNS 返回系统当前的解析器。
	InspectDNS func(ctx context.Context) ([]string, error)
	// GuardianStatus 返回 bx 自己的 TUN 名与保护状态(经 Guardian 的 /v1/status,
	// 0666,**普通用户可读** —— 这正是这个功能不需要 root 的原因之一)。
	//
	// **err == nil 的含义是「问出来了」**,不是「bx 有 TUN」:保护关着时 tun 是
	// 空串而 err 仍是 nil,那是一个确定的答案(bx 现在没有 TUN),归因据此才敢
	// 把眼前这条 utun 认成别人的。
	GuardianStatus func(ctx context.Context) (tun string, protection string, err error)
	// ListVPNServices 列系统集成 VPN(scutil --nc list)。
	ListVPNServices func(ctx context.Context) ([]leakcheck.VPNService, error)
}

// DefaultFactsBudget 给整轮本机采集封顶,数字取自 internal/cli 的 observeTimeout
// (那里的理由一字不差地适用)。
//
// **这一轮跑在用户面前**:`bx leakcheck` 要等它采完才打印那个入口 URL,采集期间
// 屏幕上什么都没有。而单项的上限加起来远不止 5 秒(`scutil --nc` 5s + `scutil
// --dns` 3s + Guardian 客户端 30s + 每个解析器一次路由查询),一个卡住的
// Guardian 就能让用户对着空屏等半分钟,然后以为命令挂了。
//
// 到点的代价是**那一项变成「没问出来」** —— 这个包对此早有诚实的答案(Unknown,
// 不是 False),所以超时是安全的那一边;超时**绝不会**被读成「确知没有 v6 路由」
// 那种能支撑一句 ok 的答案。
//
// 已知的一处溢出:`guardianTunAndProtection` 第二跳的 `supervisor.FetchRuntimeState`
// 不吃 ctx(它自带 3 秒上限),故整轮最坏会超出预算 3 秒。它有界,不会挂住。
const DefaultFactsBudget = 5 * time.Second

// CollectFacts 采一轮本机事实,整轮封顶 DefaultFactsBudget。
//
// **任一项失败只让那一项变成「没问出来」,绝不中断其余项、绝不让调用方失败**
// (与 internal/observe 同一条纪律)。零值一律读作 Unknown,不读作「没有」。
func CollectFacts(ctx context.Context, deps FactDeps) leakcheck.LocalFacts {
	return CollectFactsWithBudget(ctx, deps, DefaultFactsBudget)
}

// CollectFactsWithBudget 是 CollectFacts 带显式预算的那一版(测试用;budget <= 0
// 表示不额外封顶,调用方自己的 ctx 说了算)。
func CollectFactsWithBudget(ctx context.Context, deps FactDeps, budget time.Duration) leakcheck.LocalFacts {
	if budget > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, budget)
		defer cancel()
	}
	facts := leakcheck.LocalFacts{}

	var services []leakcheck.VPNService
	if deps.ListVPNServices != nil {
		if list, err := deps.ListVPNServices(ctx); err == nil {
			services = list
		}
		// 失败不记 Err:它只影响「翻不翻得成人话」,而 DescribeInterface 对
		// 翻不出来已经有一个诚实的答案。**尤其不许在这里塞一条假数据顶上。**
	}

	bxTun, bxTunKnown := "", false
	if deps.GuardianStatus != nil {
		tun, protection, err := deps.GuardianStatus(ctx)
		if err == nil {
			bxTun, bxTunKnown = tun, true
			facts.BXTunInterface = tun
			facts.BXProtection = protection
		}
		// Guardian 拨不通不是错误:这个功能的主场景之一就是 bx 没在跑。
		// 但它**是**「问不出来」:bxTunKnown 保持 false,归因于是不敢把眼前
		// 这条 utun 认成别人的(否则 bx 自己的 utun 会被贴上别人的名字)。
	}

	var v6Err error
	facts.DefaultRouteV4, _ = lookupInterface(ctx, deps, probeV4, false, services, bxTun, bxTunKnown)
	facts.DefaultRouteV6, v6Err = lookupInterface(ctx, deps, probeV6, true, services, bxTun, bxTunKnown)

	switch {
	case facts.DefaultRouteV6.Known():
		facts.IPv6DefaultPresent = tristate.True
	case errors.Is(v6Err, ErrNoRoute):
		// **确知**没有 v6 默认路由。只有这一支能支撑一句诚实的 ok。
		facts.IPv6DefaultPresent = tristate.False
	default:
		facts.IPv6DefaultPresent = tristate.Unknown
	}

	if deps.InspectDNS != nil {
		servers, err := deps.InspectDNS(ctx)
		if err != nil {
			facts.DNSErr = err.Error()
		} else {
			facts.DNSServers = servers
			facts.DNSServerEgress = collectDNSEgress(ctx, deps, servers)
		}
	}
	return facts
}

// collectDNSEgress 逐个问「这个解析器的包从哪个接口出去」。
//
// **每个解析器单独问一次,问不出来的那个键必须缺席。** 整条 DNS 规则读的就是
// 这张表:填一个空串会被 judgeDNS 读成「查到了,出口是空」,拿默认路由的接口名
// 顶上则会让它对一台真在漏 DNS 的机器说没问题 —— 那时这条规则就只剩装饰。
func collectDNSEgress(ctx context.Context, deps FactDeps, servers []string) map[string]string {
	egress := map[string]string{}
	if deps.LookupRoute == nil {
		// 没有查询能力 ⇒ 一个键都不填。空表读作「一个都没问出来」,而 judgeDNS
		// 对此的回答是 not checked。
		return egress
	}
	for _, server := range servers {
		// 地址族按**这个地址本身**定,不按 v4 一刀切:v6 解析器拿 v4 的问法去查
		// 只会得到一个失败,而那个失败读起来与「这台机器的 v6 DNS 没问出来」
		// 一模一样 —— 一个本来问得出的事实被自己的问法弄丢了。
		iface, err := deps.LookupRoute(ctx, server, isIPv6Literal(server))
		if err != nil || strings.TrimSpace(iface) == "" {
			continue // 少一个键 = 那个解析器的去向没问出来
		}
		egress[server] = iface
	}
	return egress
}

func isIPv6Literal(server string) bool {
	addr, err := netip.ParseAddr(strings.TrimSpace(server))
	return err == nil && addr.Is6() && !addr.Is4In6()
}

// lookupInterface 查一条路由,并把接口名翻成人话。
//
// 第二个返回值是**原始 error**:调用方要靠 errors.Is(err, ErrNoRoute) 把「确知
// 没有」与「问不出来」分开,而 InterfaceRef.Err 是给用户看的字符串,分不出来。
func lookupInterface(ctx context.Context, deps FactDeps, dest string, ipv6 bool,
	services []leakcheck.VPNService, bxTun string, bxTunKnown bool,
) (leakcheck.InterfaceRef, error) {
	if deps.LookupRoute == nil {
		err := errors.New("route lookup is not available on this platform")
		return leakcheck.InterfaceRef{Err: err.Error()}, err
	}
	iface, err := deps.LookupRoute(ctx, dest, ipv6)
	if err != nil {
		return leakcheck.InterfaceRef{Err: err.Error()}, err
	}
	if iface == "" {
		err := errors.New("route lookup returned no interface")
		return leakcheck.InterfaceRef{Err: err.Error()}, err
	}
	return leakcheck.InterfaceRef{
		Name:    iface,
		Display: leakcheck.DescribeInterface(iface, services, bxTun, bxTunKnown),
	}, nil
}
