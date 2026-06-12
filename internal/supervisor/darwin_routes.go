package supervisor

// darwin_routes.go 是 macOS Hijack 的**纯路由命令构造**(无 build tag、不执行 route,
// 故可在任意平台免 root 单测)。真正调用 `route`/检测 v6 的部分在 platform_darwin.go。

// routeSpec 是一条 macOS 路由:add 命令与对称的 del 命令(均为 `route` 的参数,不含 "route" 本身)。
type routeSpec struct {
	add []string
	del []string
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
		specs = append(specs, routeSpec{
			add: []string{"-n", "add", "-net", c, "-interface", tunName},
			del: []string{"-n", "delete", "-net", c},
		})
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
