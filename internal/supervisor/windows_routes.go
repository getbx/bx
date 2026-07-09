package supervisor

// windows_routes.go 是 Windows Hijack 的**纯路由计划构造**(无 build tag、不碰 winipcfg/syscall,
// 故可在任意平台免管理员单测)。真正用 winipcfg 应用/还原的部分在 platform_windows.go。
// 计划只描述「哪个目的地、走哪条路径(劫进 TUN / 经物理网关旁路 / v6 黑洞)」,与具体
// 编程 API 无关——applier 再把 winViaGateway 解析成物理网关 nextHop、winViaTUN/winV6Blackhole
// 解析成 on-link 的未指定地址 nextHop。

// windowsDirectCIDRs:Windows 下经物理网关旁路直连的私网段。单表平台与 darwin 同源,
// 详见 singleTableDirectCIDRs(directcidrs.go)。
var windowsDirectCIDRs = singleTableDirectCIDRs

// winRouteVia 表示一条计划路由的走向。
type winRouteVia int

const (
	winViaTUN      winRouteVia = iota // 劫进 bx TUN(gVisor 终结):on-link nextHop=0.0.0.0
	winViaGateway                     // 经物理默认网关旁路(bypass:服务器/私网/SSH)
	winV6Blackhole                    // v6 fail-closed:路由进 TUN,gVisor 无对应出站→丢弃(EHOSTUNREACH 语义)
)

// winRoute 是一条计划路由:目的前缀(CIDR 字符串,由 applier parse)+ 走向。
type winRoute struct {
	Dest string
	Via  winRouteVia
}

// windowsRoutes 纯构造 Windows Hijack 的路由计划:
//   - directCIDRs(私网/docker)+ serverBypass(防环)+ userBypass(SSH/管理)→ 经物理网关旁路;
//   - split-default(0.0.0.0/1 + 128.0.0.0/1)→ 劫进 TUN,盖住 0/0 又不动原 default(便于还原);
//   - blockV6=true 时::/1 + 8000::/1 → v6 黑洞(进 TUN 丢弃)。link-local/ULA/组播/loopback 因有
//     更具体的 on-link/本地路由按最长前缀自动直连,无需显式 carve-out。
func windowsRoutes(directCIDRs, serverBypass, userBypass []string, blockV6 bool) []winRoute {
	var routes []winRoute
	for _, groups := range [][]string{directCIDRs, serverBypass, userBypass} {
		for _, c := range groups {
			routes = append(routes, winRoute{Dest: c, Via: winViaGateway})
		}
	}
	for _, c := range []string{"0.0.0.0/1", "128.0.0.0/1"} {
		routes = append(routes, winRoute{Dest: c, Via: winViaTUN})
	}
	if blockV6 {
		for _, c := range []string{"::/1", "8000::/1"} {
			routes = append(routes, winRoute{Dest: c, Via: winV6Blackhole})
		}
	}
	return routes
}
