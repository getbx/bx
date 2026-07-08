package supervisor

import "testing"

// routeVia 把计划拍平成 dest→via 映射,便于断言某条路由的走向。
func routeVia(routes []winRoute) map[string]winRouteVia {
	m := make(map[string]winRouteVia, len(routes))
	for _, r := range routes {
		m[r.Dest] = r.Via
	}
	return m
}

// split-default(两个 /1)必须劫进 TUN;server/user/私网 bypass 必须经物理网关。
func TestWindowsRoutesSplitDefaultAndBypass(t *testing.T) {
	routes := windowsRoutes(
		[]string{"10.0.0.0/8"},
		[]string{"203.0.113.20/32"}, // 服务器防环
		[]string{"198.51.100.0/24"}, // SSH/管理源
		false,
	)
	via := routeVia(routes)

	for _, tun := range []string{"0.0.0.0/1", "128.0.0.0/1"} {
		if v, ok := via[tun]; !ok || v != winViaTUN {
			t.Errorf("%s 应劫进 TUN(winViaTUN),实得 %v ok=%v", tun, v, ok)
		}
	}
	for _, gw := range []string{"10.0.0.0/8", "203.0.113.20/32", "198.51.100.0/24"} {
		if v, ok := via[gw]; !ok || v != winViaGateway {
			t.Errorf("%s 应经物理网关旁路(winViaGateway),实得 %v ok=%v", gw, v, ok)
		}
	}
	// v6 关闭:不产任何 v6 黑洞。
	for _, r := range routes {
		if r.Via == winV6Blackhole {
			t.Errorf("blockV6=false 不应产 v6 黑洞: %q", r.Dest)
		}
	}
}

// blockV6=true:::/1 + 8000::/1 → v6 黑洞;v4 split-default 零回归。
func TestWindowsRoutesBlocksV6(t *testing.T) {
	routes := windowsRoutes([]string{"10.0.0.0/8"}, []string{"1.2.3.4/32"}, nil, true)
	via := routeVia(routes)

	for _, v6 := range []string{"::/1", "8000::/1"} {
		if v, ok := via[v6]; !ok || v != winV6Blackhole {
			t.Errorf("%s 应为 v6 黑洞(winV6Blackhole),实得 %v ok=%v", v6, v, ok)
		}
	}
	if v, ok := via["0.0.0.0/1"]; !ok || v != winViaTUN {
		t.Error("v6 改动不应影响 v4 split-default 劫持")
	}
}

// blockV6=false:一条 v6 路由都不产(不连累 v4)。
func TestWindowsRoutesSkipsV6WhenDisabled(t *testing.T) {
	routes := windowsRoutes(windowsDirectCIDRs, nil, nil, false)
	for _, r := range routes {
		if r.Dest == "::/1" || r.Dest == "8000::/1" || r.Via == winV6Blackhole {
			t.Errorf("blockV6=false 不应产 v6 路由: %q", r.Dest)
		}
	}
	if v, ok := routeVia(routes)["0.0.0.0/1"]; !ok || v != winViaTUN {
		t.Error("v4 split-default 应仍在")
	}
}

// 单路由表(同 macOS):不认领 CGNAT(100.64/10),其余私网直连零回归。
func TestWindowsDoesNotClaimCGNAT(t *testing.T) {
	via := routeVia(windowsRoutes(windowsDirectCIDRs, nil, nil, false))
	if _, ok := via["100.64.0.0/10"]; ok {
		t.Error("Windows 不应认领 CGNAT 100.64.0.0/10(与 tailscale overlay 同前缀冲突)")
	}
	for _, c := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"} {
		if via[c] != winViaGateway {
			t.Errorf("私网直连零回归丢失: %q", c)
		}
	}
}
