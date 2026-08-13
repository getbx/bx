package supervisor

import (
	"context"
	"fmt"
	"log"
	"net/netip"
	"os"
	"time"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/dialer"
)

// bypassRefreshDeps 是「切换前刷新 bypass」的全部外部依赖。
// 抽成结构体是为了让这段逻辑**可测**:复审在它内部做过四处变异
// (changed 恒 false、删掉写回、最后一跳把 requiredLinks 换成 nil、
// WithTimeout 换 WithCancel),每一处都制造出静默成环,而当时整套测试全绿
// —— 因为它是个捕获局部变量的闭包,没有任何测试能碰到它。
type bypassRefreshDeps struct {
	configPath string
	// resolve 必须是**防环的**解析器(经国内 DNS 直连出去),不能是系统解析器:
	// 刷新发生在 DNS 已交给 bx 之后,系统解析器就是 bx 自己,新服务器域名会被
	// 回一个 fake IP。
	resolve func(context.Context, string) ([]netip.Addr, error)
	store   *bypassStore
	// setStaticA 把静态 DNS 那一半推给 dns.Server(可空,单测常留空)。
	setStaticA func(map[string][]netip.Addr)
	// extraCIDRs 是与「切到哪台服务器」无关的旁路(tailscale 共存那组)。
	// **每轮现读**:它由后台重试循环维护(tailscaleBypassSource),失败时保留上一份、
	// 绝不缩小(decideBypassUpdate)。此前这里是一份冻结切片,理由是「重探一次失败
	// 就会悄悄缩小旁路」—— 那条担心是对的,但用「永不重探」去满足它,代价是开机自启
	// 抓不到 DERP map 时兜底表会一直用到下次重启。现在担心由 decideBypassUpdate 承担。
	extraCIDRs func() []string
	fakeipCIDR string
	timeout    time.Duration
}

// newBypassRefresher 造出「切换前刷新 bypass」那个函数。
//
// 它重读配置、重算 bypass 两半并发布到 store,回报**路由集合是否变了**
// (变了端点才需要 rehijack)。requiredLinks 是调用方点名的切换目标:
// 端点自己知道要切到哪台,不从配置文件的 current 去猜(spec 是「先热切、
// 成功后才落盘」,猜必然错一次,而错的那一次就是不装 bypass 直接切过去 = 成环)。
func newBypassRefresher(d bypassRefreshDeps) func(context.Context, []string) (bool, error) {
	return func(ctx context.Context, requiredLinks []string) (bool, error) {
		if d.configPath == "" {
			return false, fmt.Errorf("未知配置路径,无法刷新 bypass")
		}
		raw, err := os.ReadFile(d.configPath)
		if err != nil {
			return false, fmt.Errorf("重读配置: %w", err)
		}
		fresh, err := config.Parse(raw)
		if err != nil {
			return false, fmt.Errorf("重读配置: %w", err)
		}

		timeout := d.timeout
		if timeout <= 0 {
			timeout = bypassRefreshTimeout
		}
		// 整轮解析封顶:这段跑在控制面的 cs.mu 里,解析器一挂就会把
		// /v0/commit、/v0/transport、/v0/rehijack 一起连坐。
		rctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		fakeip, fakeipErr := netip.ParsePrefix(nonEmpty(d.fakeipCIDR, fresh.DNS.FakeipCIDR))
		resolve := func(host string) []netip.Addr {
			addrs, err := d.resolve(rctx, host)
			if err != nil {
				return nil
			}
			if fakeipErr != nil {
				return addrs
			}
			return dropFakeIPs(host, addrs, fakeip)
		}

		// 保留上一轮解析成功的地址,只用于**配置里仍然写着**的主机:
		// 一台已知服务器这轮 DNS 抖一下不该掉出 bypass(掉出去之后 runFailover
		// 随时可能切到它 = 成环);而用户删掉的服务器必须真的消失(留着 =
		// 它的流量绕开隧道)。「仍然写着」这一条由遍历范围天然保证 —— 只有
		// 从 fresh 配置(和调用方点名)推出的主机才会被查到。
		//
		// 基准取 serverEntries() 而**不是** staticEntries():后者含用户 `hosts:`
		// 覆盖,一个先在 hosts 里、后来变成传输服务器的域名一旦这轮解析失败,
		// 用户配的那个任意 IPv4 就会被「保留」成服务器地址,同时进静态表与
		// Guardian 屏障开口 —— 而 mergeHostOverrides 在同一次刷新里正拒绝着它。
		retain := d.store.serverEntries()
		serverStatic, addrs, err := resolveServerBypassRetaining(fresh, requiredLinks, resolve, retain)
		if err != nil {
			return false, err
		}

		// 用户 hosts 覆盖在启动时是合并进静态表的,刷新不能把它抹掉。
		userHosts, err := fresh.HostOverrides()
		if err != nil {
			return false, fmt.Errorf("hosts 覆盖: %w", err)
		}
		staticA, _, ignored := mergeHostOverrides(serverStatic, userHosts)
		for _, host := range ignored {
			log.Printf("hosts_override_ignored host=%s reason=transport_server", host)
		}

		var extra []string
		if d.extraCIDRs != nil {
			extra = d.extraCIDRs()
		}
		next := mergeBypassCIDRs(addrsToCIDRs(addrs), extra)
		changed := !equalStringSets(d.store.cidrs(), next)
		// 两半一起发布,且**无论 changed 与否都发布**:路由集合可以没变而静态
		// DNS 变了(同一台服务器换了 IP 之外的记录),漏发布就等于隧道子进程
		// 仍拿旧答案。
		// serverStatic 是合并用户 hosts **之前**的那一半,屏障开口与下一轮的
		// 保留基准用的正是它。
		d.store.set(next, staticA, serverStatic)
		if d.setStaticA != nil {
			d.setStaticA(staticA)
		}
		return changed, nil
	}
}

// dropFakeIPs 剔掉落在 fake-IP 段里的应答。
//
// 给一个 fake IP 装 bypass 路由永远是错的:真实服务器 IP 一次都没进过 bypass
// (隧道连不上),而那条 /32 会一直留在集合里,之后任何一次 rehijack 都会把它
// 重新装上 —— 那个域名就此被黑洞。宁可当作「解析失败」拒绝切换。
func dropFakeIPs(host string, addrs []netip.Addr, fakeip netip.Prefix) []netip.Addr {
	var out []netip.Addr
	for _, a := range addrs {
		if fakeip.Contains(a.Unmap()) {
			log.Printf("bypass 刷新丢弃 fake-IP 应答 host=%s addr=%s(解析器指向了 bx 自己)", host, a)
			continue
		}
		out = append(out, a)
	}
	return out
}

func nonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// resolveAllResolver 是解析器可选实现的「给我全部地址」能力。
type resolveAllResolver interface {
	ResolveAll(ctx context.Context, domain string) ([]netip.Addr, error)
}

// resolveAll 把 dialer.Resolver 适配成 bypass 刷新要的「一个 host → 全部 IP」。
//
// 尽量取**全部**:服务器域名有多条 A 记录时,bypass 只覆盖其中一条而子进程
// 连了另一条就是成环。解析器给不了全部才退回单条。
// IP 字面量直接短路 —— 那种 link 根本不需要解析器,解析器坏了也不该连累它们。
func resolveAll(ctx context.Context, r dialer.Resolver, host string) ([]netip.Addr, error) {
	if addr, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{addr.Unmap()}, nil
	}
	if all, ok := r.(resolveAllResolver); ok {
		addrs, err := all.ResolveAll(ctx, host)
		if err != nil {
			return nil, err
		}
		if len(addrs) > 0 {
			return addrs, nil
		}
	}
	addr, err := r.Resolve(ctx, host)
	if err != nil {
		return nil, err
	}
	return []netip.Addr{addr.Unmap()}, nil
}

// bypassWiringParams 是把 bypass 那套接起来所需的全部输入。
type bypassWiringParams struct {
	configPath string
	// serverStatics 是传输服务器那一半的静态表,**合并用户 hosts 覆盖之前**的那份。
	// 屏障开口与刷新的保留基准用的都是它:混进 hosts 覆盖的 IP 等于在断网窗口里
	// 给一个任意 IPv4 打洞(hosts: 接受任何 IPv4 字面量,不限内网)。
	serverStatics map[string][]netip.Addr
	// staticA 是发布给 DNS 的静态表,**已经**合并过用户 hosts 覆盖。
	// 它与 serverStatics 刻意分开传:一个是「谁能穿过屏障」,一个是「谁有静态答案」,
	// 从后者推前者正是 a8c670f 引入的那个洞。
	staticA map[string][]netip.Addr
	// extraCIDRs 是**活的**:tailscale 那组中继旁路在启动时抓不到就退回内置兜底表,
	// 而后台会一直重试。冻结成一份切片正是原来的缺陷 —— 开机自启时网络往往还没好,
	// 于是那份兜底表会一直用到下次重启。
	extraCIDRs func() []string
	// resolver 必须是防环解析器(国内 DNS + DirectDialer),**绝不能**是系统解析器:
	// 刷新发生在 DNS 已交给 bx 之后,系统解析器此刻就是 bx 自己。
	resolver   dialer.Resolver
	setStaticA func(map[string][]netip.Addr)
	fakeipCIDR string
}

// bypassWiring 是接好之后的那几个口子。
type bypassWiring struct {
	store               *bypassStore
	refresh             func(context.Context, []string) (bool, error)
	runtimeServerBypass func() []string
}

// wireBypass 把「什么必须绕开隧道」的**组合**集中到一处。
//
// 为什么要有这个函数:被抽出来的每个单元都有测试,而把它们连起来的那几行
// 没有任何东西盯着 —— 而本轮两个 Critical(经 bx 自己的 fake-IP DNS 解析、
// 屏障开口混进用户 hosts 覆盖)**都是接线错误**,不是单元错误。
func wireBypass(p bypassWiringParams) bypassWiring {
	extraNow := func() []string {
		if p.extraCIDRs == nil {
			return nil
		}
		return p.extraCIDRs()
	}
	store := newBypassStore(
		mergeBypassCIDRs(addrsToCIDRs(flattenServerAddrs(p.serverStatics)), extraNow()),
		p.staticA,
		p.serverStatics,
	)
	refresh := newBypassRefresher(bypassRefreshDeps{
		configPath: p.configPath,
		resolve: func(ctx context.Context, host string) ([]netip.Addr, error) {
			return resolveAll(ctx, p.resolver, host)
		},
		store:      store,
		setStaticA: p.setStaticA,
		extraCIDRs: extraNow,
		fakeipCIDR: p.fakeipCIDR,
	})
	return bypassWiring{
		store:   store,
		refresh: refresh,
		// 现算,不冻:RuntimeState.ServerBypass 经 cli/guardian.go 变成 Guardian
		// 屏障的开口,冻住等于切过服务器之后放行的还是旧那台。
		runtimeServerBypass: func() []string { return runtimeIPv4Bypass(store.serverAddrs()) },
	}
}
