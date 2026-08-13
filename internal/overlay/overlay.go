// Package overlay 把「机器上还跑着别的 overlay 网络」这件事做成**一张声明式的表**,
// 而不是散在各处的 if。
//
// 为什么要有它:Tailscale 此前是三处特例(私网段里塞 CGNAT、fakeip_filter 里塞两个
// 域名、一个专门抓 DERP map 的模块),而 ZeroTier 在数据面**一行代码都没有** ——
// 只有只读检测。加第三个 overlay 就要再散三处,而没有任何东西保证它们一致。
//
// 表里一行 = 一个租户,声明四件事:怎么认出它在跑、它的地址空间要不要额外直连、
// 它的控制面/中继在哪、它自己的命名空间该交给谁解析。判据全是纯函数,表驱动可测。
//
// **本包不做 I/O**:检测所需的信号(接口名、地址)由调用方采好传进来。
package overlay

import (
	"net/netip"
	"sort"
	"strings"
)

// Tenant 是一个与 bx 共存的 overlay 网络。
type Tenant struct {
	Name string

	// IfacePrefixes / OverlayPrefix 是**两种检测信号**,命中任一即认为它在跑。
	//
	// 两种都要,因为哪种可用**随平台变**:Tailscale 在 Linux 上叫 tailscale0,
	// 在 macOS 上却是 utunN —— 与其它任何隧道无法区分,所以那里只能靠地址;
	// ZeroTier 的地址是 RFC1918,与局域网无法区分,所以只能靠接口名。
	// 把两种信号都放在表里,是为了让「这个平台上认不出来」成为一件**看得见**的事。
	IfacePrefixes []string
	// OverlayPrefix 非零时,任一接口上出现该段内的地址即认定它在跑。
	OverlayPrefix netip.Prefix

	// DirectCIDRs 是必须额外声明直连的地址空间。
	//
	// **为空是信息,不是遗漏**:ZeroTier 默认发 RFC1918 地址,而那些段本来就在
	// route.DefaultPrivateCIDRs 里恒直连 —— 它不需要特例,恰恰因为它守规矩。
	// Tailscale 用 CGNAT(100.64/10),不在 RFC1918 里,所以必须专门声明。
	DirectCIDRs []string

	// RelayHosts 是控制面/中继的**主机名**,这是稳定的那一半;RelayFallbackCIDRs
	// 是写这张表时核对过的地址,只在解析不出来时兜底。
	//
	// 上游文档明说 root IP 会变(少见但会),推荐解析主机名 —— 所以主机名是
	// 权威来源,IP 只是启动那几秒的垫脚石。**写死的公网 IP 是一种泄漏面**:
	// 地址被回收给别人之后,那条旁路仍在,于是发往陌生人的流量绕过隧道。
	// 缓解是它只在这个租户**确实在跑**时才装(见 BypassCIDRs)。
	RelayHosts         []string
	RelayFallbackCIDRs []string

	// DNSSuffixes → Resolver:它自己的命名空间该交给谁。
	//
	// 只在租户在跑时生效:解析器是它自己起的,它没跑时把查询送过去只会挂住。
	DNSSuffixes []string
	Resolver    string
}

// Tenants 是全部已知租户。**加一个 overlay = 加一行 + 一条用例。**
func Tenants() []Tenant {
	return []Tenant{
		{
			Name: "tailscale",
			// Linux 上 tailscaled 建的接口叫 tailscale0;macOS 上是 utunN,
			// 与任何别的隧道同名,故那里只能靠 CGNAT 地址认。
			IfacePrefixes: []string{"tailscale"},
			OverlayPrefix: netip.MustParsePrefix("100.64.0.0/10"),
			// CGNAT 不在 RFC1918,通用私网规则盖不住,必须显式直连。
			DirectCIDRs: []string{"100.64.0.0/10"},
			RelayHosts:  []string{"controlplane.tailscale.com"},
			// DERP 中继由 supervisor 抓 DERP map 动态得到(见 tailscale_bypass.go),
			// 不在这里写死。
			DNSSuffixes: []string{"ts.net"},
			// MagicDNS 的固定地址,Tailscale 官方文档长期稳定。
			Resolver: "100.100.100.100",
		},
		{
			Name: "zerotier",
			// zt* 是 Linux/BSD 上的接口名。macOS 上 ZeroTier 用 feth 对,
			// 而 feth 是系统通用的「假以太网」设备 —— 认它会误报。
			// **这里刻意只认 zt**:误报的代价是给几条陌生 /32 开旁路,
			// 而漏报的代价只是维持现状(今天 ZeroTier 本来就一条旁路都没有)。
			IfacePrefixes: []string{"zt"},
			// 地址空间是 RFC1918,**没有可用的地址信号**,也不需要额外直连。
			RelayHosts: []string{
				"root-lax-01.zerotier.com",
				"root-mia-01.zerotier.com",
				"root-sgp-01.zerotier.com",
				"root-zrh-01.zerotier.com",
				"root-alice-sfo-01.zerotier.com",
			},
			// 2026-08-12 核对自 ZeroTier 官方 whitelist 文档。**它们会变** ——
			// 所以只在解析不出主机名时兜底,而且只在 ZeroTier 在跑时才装。
			RelayFallbackCIDRs: []string{
				"104.194.8.134/32",
				"103.195.103.66/32",
				"50.7.252.138/32",
				"84.17.53.155/32",
				"107.170.197.14/32",
			},
			// ZeroTier 默认不提供自己的 DNS 命名空间(DNS push 是可选的,
			// 而且没有固定解析器地址),故这一栏为空。
		},
	}
}

// Signals 是检测所需的本机事实。**由调用方采好传进来**,本包不做 I/O。
type Signals struct {
	InterfaceNames []string
	// Addresses 是各接口上的地址(任何族)。
	Addresses []netip.Addr
}

// Detect 返回看起来正在这台机器上跑的租户。
//
// **命中任一信号即认定在跑**,而不是要求全部命中:两种信号的可用性随平台变,
// 要求全中等于在每个平台上都认不出至少一个租户。
func Detect(signals Signals) []Tenant {
	var present []Tenant
	for _, tenant := range Tenants() {
		if tenantPresent(tenant, signals) {
			present = append(present, tenant)
		}
	}
	return present
}

func tenantPresent(tenant Tenant, signals Signals) bool {
	for _, name := range signals.InterfaceNames {
		name = strings.ToLower(strings.TrimSpace(name))
		for _, prefix := range tenant.IfacePrefixes {
			if name != "" && strings.HasPrefix(name, prefix) {
				return true
			}
		}
	}
	if tenant.OverlayPrefix.IsValid() {
		for _, addr := range signals.Addresses {
			if tenant.OverlayPrefix.Contains(addr) {
				return true
			}
		}
	}
	return false
}

// DirectCIDRs 汇总在跑的租户要求额外直连的地址空间。
//
// 注意它**不包含** route.DefaultPrivateCIDRs:那一份是无条件的,与租户在不在跑无关。
// 这里只出「因为某个 overlay 在跑,所以额外要直连」的那部分。
func DirectCIDRs(present []Tenant) []string {
	return collect(present, func(t Tenant) []string { return t.DirectCIDRs })
}

// BypassCIDRs 汇总在跑的租户的中继兜底地址。
//
// **只对在跑的租户出**:写死的公网 IP 是泄漏面(地址被回收给别人后那条旁路仍在),
// 按在跑与否门控把影响面限制在真正需要它的机器上。主机名那一半由调用方解析,
// 解析得到的结果应当**压过**这里的兜底值。
func BypassCIDRs(present []Tenant) []string {
	return collect(present, func(t Tenant) []string { return t.RelayFallbackCIDRs })
}

// RelayHosts 汇总在跑的租户的中继主机名,供调用方解析成实时地址。
func RelayHosts(present []Tenant) []string {
	return collect(present, func(t Tenant) []string { return t.RelayHosts })
}

// SplitRoute 是「这个后缀交给这个解析器」。
type SplitRoute struct {
	Suffix   string
	Resolver string
}

// SplitRoutes 汇总在跑的租户自己的命名空间该交给谁解析。
//
// **只对在跑的租户出**:解析器是租户自己起的进程,它没跑时把查询送过去只会挂住 ——
// 那比解析不出来更糟(前者是超时,后者至少立刻失败)。
func SplitRoutes(present []Tenant) []SplitRoute {
	var routes []SplitRoute
	for _, tenant := range present {
		if strings.TrimSpace(tenant.Resolver) == "" {
			continue
		}
		for _, suffix := range tenant.DNSSuffixes {
			routes = append(routes, SplitRoute{Suffix: suffix, Resolver: tenant.Resolver})
		}
	}
	sort.Slice(routes, func(i, j int) bool { return routes[i].Suffix < routes[j].Suffix })
	return routes
}

func collect(present []Tenant, pick func(Tenant) []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, tenant := range present {
		for _, v := range pick(tenant) {
			if v = strings.TrimSpace(v); v != "" && !seen[v] {
				seen[v] = true
				out = append(out, v)
			}
		}
	}
	sort.Strings(out)
	return out
}
