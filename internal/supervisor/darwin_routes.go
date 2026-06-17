package supervisor

import "strings"

// darwin_routes.go 是 macOS Hijack 的**纯路由命令构造**(无 build tag、不执行 route,
// 故可在任意平台免 root 单测)。真正调用 `route`/检测 v6 的部分在 platform_darwin.go。

// darwinDirectCIDRs:macOS 下保持原生直连(经物理网关)的私网段——RFC1918 + docker。
// 刻意不含 loopback(127/8)与 link-local(169.254/16):它们已有正确的本地路由,绝不可改写。
// 刻意不含 CGNAT(100.64.0.0/10):macOS 单路由表下,把它 route → 物理网关会和 tailscale 的
// overlay 路由(100.64.0.0/10 → tailscale utun,同前缀)冲突,断掉主动连 tailscale peer。
// tailscale 的 100.64/10 比 split-default 的 0/1 更具体,按最长前缀自然抢赢,无需 bx 认领;
// 无 tailscale 时本地 CGNAT 子网亦有更具体的 connected 路由直连,故不漏。
// （放在无 build tag 的本文件,便于在任意平台免 root 单测。）
var darwinDirectCIDRs = []string{
	"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16",
}

// routeSpec 是一条 macOS 路由:add 命令与对称的 del 命令(均为 `route` 的参数,不含 "route" 本身)。
type routeSpec struct {
	add []string
	del []string
}

// DarwinRoutePlanOptions 是 macOS 路由 dry-run 的输入。它只用于生成命令文本,不执行任何命令。
type DarwinRoutePlanOptions struct {
	TunName      string
	TunAddr      string
	Gateway      string
	ServerBypass []string
	UserBypass   []string
	BlockV6      bool
}

// DarwinRoutePlan 生成 macOS Hijack 将执行的命令和对称清理命令,供真机验证前审计。
func DarwinRoutePlan(opts DarwinRoutePlanOptions) (apply []string, cleanup []string) {
	tunIP := opts.TunAddr
	if i := strings.IndexByte(tunIP, '/'); i >= 0 {
		tunIP = tunIP[:i]
	}
	apply = append(apply, commandString("ifconfig", opts.TunName, "inet", tunIP, tunIP, "up"))
	specs := darwinRouteSpecs(opts.TunName, opts.Gateway, darwinDirectCIDRs, opts.ServerBypass, opts.UserBypass, opts.BlockV6)
	for _, s := range specs {
		apply = append(apply, commandString("route", s.add...))
	}
	for i := len(specs) - 1; i >= 0; i-- {
		cleanup = append(cleanup, commandString("route", specs[i].del...))
	}
	return apply, cleanup
}

func commandString(name string, args ...string) string {
	return strings.Join(append([]string{name}, args...), " ")
}

// darwinRouteSpecs 纯构造 macOS Hijack 的全部 route 命令序列:
//   - v4:directCIDRs(私网/docker)+ serverBypass + userBypass 经物理网关 gw 旁路;
//     split-default(0.0.0.0/1 + 128.0.0.0/1)把默认流量劫进 tunName(utun)。
//   - v6(仅 blockV6=true):两个 /1 的 `-reject` 盖全量全局 v6 —— fail-closed 阻断,
//     本地发送者得 EHOSTUNREACH(逼双栈应用快速回落 v4),与 Linux 的 `unreachable` 决策一致。
//     link-local(fe80::/10)、ULA on-link、组播(ff00::/8)、loopback(::1)因有更具体的
//     on-link/本地路由,按最长前缀匹配自动抢赢直连,无需显式 carve-out(亦绝不可改写本地路由)。
//
// ⚠️ `-reject` 的确切 route 语法(dummy gateway `::1`)与本地 errno 需在真实 macOS 上验证。
func darwinRouteSpecs(tunName, gw string, directCIDRs, serverBypass, userBypass []string, blockV6 bool) []routeSpec {
	var specs []routeSpec
	viaGW := func(cidr string) routeSpec {
		return routeSpec{
			add: []string{"-n", "add", "-net", cidr, gw},
			del: []string{"-n", "delete", "-net", cidr},
		}
	}
	viaTun := func(cidr string) routeSpec {
		return routeSpec{
			add: []string{"-n", "add", "-net", cidr, "-interface", tunName},
			del: []string{"-n", "delete", "-net", cidr},
		}
	}
	for _, c := range directCIDRs {
		specs = append(specs, viaGW(c))
	}
	for _, c := range serverBypass {
		specs = append(specs, viaGW(c))
	}
	for _, c := range userBypass {
		specs = append(specs, viaGW(c))
	}
	for _, c := range []string{"0.0.0.0/1", "128.0.0.0/1"} {
		specs = append(specs, viaTun(c))
	}
	if blockV6 {
		for _, c := range []string{"::/1", "8000::/1"} {
			specs = append(specs, routeSpec{
				add: []string{"-n", "add", "-inet6", "-net", c, "::1", "-reject"},
				del: []string{"-n", "delete", "-inet6", "-net", c},
			})
		}
	}
	return specs
}
