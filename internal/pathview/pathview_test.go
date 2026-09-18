package pathview

import (
	"net/netip"
	"strings"
	"testing"
)

// `bx explain` 的本机视角:「这个目标在这台机器上会怎么走」,bx 在不在跑都能答。
// 动机是同一天两次误判:另一会话看到 `8.8.8.8 → utun9` 就断定 bx 吞了 Tailscale,
// 真相是普通进程走 bx 的 TUN、绑了网卡的进程走 en0 —— 两个视角并排摆出来,
// 那个结论当时就站不住。判据全部纯函数,I/O 在 cli 那一侧。

func facts() Facts {
	return Facts{
		Target:      "8.8.8.8",
		LiteralIP:   true,
		Addrs:       []netip.Addr{netip.MustParseAddr("8.8.8.8")},
		FakeIP:      netip.MustParsePrefix("198.18.0.0/15"),
		Route:       RouteFact{Applicable: true, Interface: "utun0"},
		Bound:       RouteFact{Applicable: true, Interface: "en0", Gateway: "192.168.50.2"},
		PhysicalDev: "en0",
		BxTun:       "utun0",
		BxTunKnown:  true,
		CoreRunning: true,
		China:       func(netip.Addr) bool { return false },
	}
}

func TestPublicTargetIntoBxWithBoundSocketsEscaping(t *testing.T) {
	v := Judge(facts())
	if !strings.Contains(v.Conclusion, "into bx") || !strings.Contains(v.Conclusion, "en0") {
		t.Fatalf("结论要同时说出两个视角(普通进程进 bx、绑网卡的从 en0 直出): %q", v.Conclusion)
	}
	if v.Kind != KindPublic {
		t.Fatalf("Kind = %q, want public", v.Kind)
	}
}

func TestServerBypassGoesOutPhysicalAndSaysWhy(t *testing.T) {
	f := facts()
	f.Target, f.Addrs = "203.0.113.92", []netip.Addr{netip.MustParseAddr("203.0.113.92")}
	f.Route = RouteFact{Applicable: true, Interface: "en0", Gateway: "192.168.50.2"}
	f.ServerBypass = []netip.Prefix{netip.MustParsePrefix("203.0.113.92/32")}
	v := Judge(f)
	if !strings.Contains(v.Conclusion, "en0") || !strings.Contains(v.Conclusion, "not through bx") {
		t.Fatalf("旁路目标要说「从 en0 直出、不经过 bx」: %q", v.Conclusion)
	}
	if !strings.Contains(v.Conclusion, "server bypass") {
		t.Fatalf("要说出原因是服务器旁路: %q", v.Conclusion)
	}
	if v.Kind != KindServerBypass {
		t.Fatalf("Kind = %q", v.Kind)
	}
}

func TestPrivateTargetIsAlwaysDirect(t *testing.T) {
	f := facts()
	f.Target, f.Addrs = "192.168.50.1", []netip.Addr{netip.MustParseAddr("192.168.50.1")}
	f.Route = RouteFact{Applicable: true, Interface: "en0"}
	v := Judge(f)
	if v.Kind != KindPrivate || !strings.Contains(v.Conclusion, "private network") {
		t.Fatalf("私网要说私网: kind=%q %q", v.Kind, v.Conclusion)
	}
}

func TestCGNATTargetNamesTailscaleRange(t *testing.T) {
	f := facts()
	f.Target, f.Addrs = "100.101.160.96", []netip.Addr{netip.MustParseAddr("100.101.160.96")}
	f.Route = RouteFact{Applicable: true, Interface: "utun10"}
	v := Judge(f)
	if v.Kind != KindCGNAT || !strings.Contains(v.Conclusion, "utun10") || strings.Contains(v.Conclusion, "into bx") {
		t.Fatalf("CGNAT 段走别的隧道,不该说进 bx: kind=%q %q", v.Kind, v.Conclusion)
	}
}

func TestFakeIPResolutionSaysDNSIsBxOwned(t *testing.T) {
	f := facts()
	f.Target, f.LiteralIP = "www.google.com", false
	f.Addrs = []netip.Addr{netip.MustParseAddr("198.18.0.40")}
	v := Judge(f)
	if !hasLine(v, "Resolves", "fake IP") {
		t.Fatalf("解析到 fake-IP 要明说 DNS 归 bx: %+v", v.Lines)
	}
	if v.Kind != KindFakeIP {
		t.Fatalf("Kind = %q", v.Kind)
	}
}

func TestRealResolutionSaysDNSIsNotBxOwned(t *testing.T) {
	f := facts()
	f.Target, f.LiteralIP = "www.baidu.com", false
	f.Addrs = []netip.Addr{netip.MustParseAddr("110.242.68.66")}
	f.China = func(a netip.Addr) bool { return a == netip.MustParseAddr("110.242.68.66") }
	f.Route = RouteFact{Applicable: true, Interface: "en0"}
	f.CoreRunning = false
	f.BxTunKnown = false
	v := Judge(f)
	if !hasLine(v, "Resolves", "a real IP") || hasLine(v, "Resolves", "not owned by bx") {
		t.Fatalf("解析到真 IP 只说「真 IP」,不断言 DNS 归属(fakeip_filter/hosts 下 bx 也会答真 IP): %+v", v.Lines)
	}
	if v.Kind != KindChina {
		t.Fatalf("Kind = %q, want china", v.Kind)
	}
	if !strings.Contains(v.Conclusion, "bx is not running") {
		t.Fatalf("Core 没在跑要在结论里说出来: %q", v.Conclusion)
	}
}

func TestRejectRouteIsNamedAsBlocked(t *testing.T) {
	f := facts()
	f.Route = RouteFact{Applicable: true, Reject: true}
	v := Judge(f)
	if !strings.Contains(v.Conclusion, "blocking route") {
		t.Fatalf("reject 路由要说被阻断: %q", v.Conclusion)
	}
}

func TestUnknownsAreSaidNotGuessed(t *testing.T) {
	f := facts()
	f.Route = RouteFact{Applicable: true, Err: "route: command timed out"}
	v := Judge(f)
	if !strings.Contains(v.Conclusion, "Could not find out") {
		t.Fatalf("路由问不出来要说问不出,不许猜: %q", v.Conclusion)
	}
	f = facts()
	f.Addrs, f.LiteralIP, f.ResolveErr = nil, false, "no such host"
	f.Target = "nonexistent.invalid"
	v = Judge(f)
	if !hasLine(v, "Resolves", "failed") {
		t.Fatalf("解析失败要明说: %+v", v.Lines)
	}
}

func TestBoundRouteMissingSaysBoundSocketsFail(t *testing.T) {
	f := facts()
	f.Bound = RouteFact{Applicable: true, Missing: true}
	v := Judge(f)
	if !strings.Contains(v.Conclusion, "NIC-bound program") || !strings.Contains(v.Conclusion, "cannot reach it") {
		t.Fatalf("scoped 表里没路由 ⇒ 绑网卡的程序连不上,要说出来: %q", v.Conclusion)
	}
}

func TestUnknownInterfaceIsNotCalledPhysical(t *testing.T) {
	f := facts()
	f.Route = RouteFact{Applicable: true, Interface: "bridge7"}
	f.PhysicalDev = "en0"
	v := Judge(f)
	// 只看主句:绑网卡那一句说的是另一种 socket,它说 en0 直出是对的。
	main, _, _ := strings.Cut(v.Conclusion, " A NIC-bound program")
	if strings.Contains(main, "real IP") {
		t.Fatalf("认不出的接口不许说成物理网卡直出: %q", main)
	}
	if !strings.Contains(v.Conclusion, "bridge7") {
		t.Fatalf("要把接口名说出来: %q", v.Conclusion)
	}
}

func hasLine(v View, label, want string) bool {
	for _, l := range v.Lines {
		if l.Label == label && strings.Contains(l.Text, want) {
			return true
		}
	}
	return false
}

// 假 IP 目标:绑了网卡的程序拿到的是假 IP,从 en0 发出去石沉大海 —— 这正是
// 2026-09-03 Tailscale 自建 DERP 域名那次(dial 198.18.0.40 超时)。说成
// 「从 en0 直出,源 IP 是真实 IP」是错的,要说它连不上、该怎么办。
func TestFakeIPBoundSocketsAreToldTheyWillDie(t *testing.T) {
	f := facts()
	f.Target, f.LiteralIP = "brook.example.com", false
	f.Addrs = []netip.Addr{netip.MustParseAddr("198.18.0.40")}
	v := Judge(f)
	if strings.Contains(v.Conclusion, "real IP") {
		t.Fatalf("假 IP 目标不许说绑网卡的程序会带真实 IP 直出: %q", v.Conclusion)
	}
	if !strings.Contains(v.Conclusion, "NIC-bound program") || !strings.Contains(v.Conclusion, "fakeip_filter") {
		t.Fatalf("要说出绑网卡的程序拿到假 IP 会连不上、以及出路(fakeip_filter/hosts/写 IP): %q", v.Conclusion)
	}
}

// CGNAT 目标走的是 overlay 自己的隧道,底层绑网卡那一句对它是噪声:Tailscale
// 不会把 100.x 从 en0 发出去。
func TestCGNATTargetDoesNotGetTheBoundSocketClause(t *testing.T) {
	f := facts()
	f.Target, f.Addrs = "100.101.160.96", []netip.Addr{netip.MustParseAddr("100.101.160.96")}
	f.Route = RouteFact{Applicable: true, Interface: "utun10"}
	v := Judge(f)
	if strings.Contains(v.Conclusion, "NIC-bound program") {
		t.Fatalf("CGNAT 目标不该带绑网卡那一句: %q", v.Conclusion)
	}
}
