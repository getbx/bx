package supervisor

import (
	"context"
	"net/netip"
	"reflect"
	"testing"

	"github.com/getbx/bx/internal/stats"
)

// 真机 2026-09-28:菜单红字「Outside bx: ssh」,而那条 ssh 连的是 bx 自己的 VPS ——
// 服务器旁路 /32 把它送去 en0 是 bx 的设计。判据(appattr.StrayConnections)要知道
// bx 自己绕开了哪些网段,而这份清单 Core 手里本来就有:RuntimeState.ServerBypass
// (现算的,切服务器之后跟着变)与配置里的 bypass:。这里守的是「递到判据手上的
// 是这两份、而且是活的」,不是「调用发生过」。
func TestRoutedAroundForGuardJoinsTheLiveServerBypassWithTheUserBypass(t *testing.T) {
	server := []string{"203.0.113.92/32"}
	opts := controlServeOptions{
		Runtime:    func() RuntimeState { return RuntimeState{ServerBypass: server} },
		UserBypass: []string{"10.0.0.0/8", "198.51.100.77", " ", "not-a-cidr"},
	}
	routedAround := routedAroundForGuard(opts)
	want := []netip.Prefix{
		netip.MustParsePrefix("203.0.113.92/32"),
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("198.51.100.77/32"), // 裸 IP 与 Hijack 一样补成 /32
	}
	if got := routedAround(); !samePrefixes(got, want) {
		t.Fatalf("routed-around = %v, want %v (garbage entries dropped, nothing invented)", got, want)
	}
	// 切过服务器之后 ServerBypass 变了,判据看到的必须跟着变 —— 冻在启动值上等于
	// 切换之后旧服务器免检、新服务器被点名。
	server = []string{"203.0.113.93/32"}
	if got := routedAround(); containsPrefix(got, want[0]) || !containsPrefix(got, netip.MustParsePrefix("203.0.113.93/32")) {
		t.Fatalf("after a server switch routed-around = %v, want the new server and not the old one", got)
	}
}

// 组装根:守卫拿到的 routedAround 必须就是上面那份 —— 这一跳漏了(传 nil、传别的),
// 类型系统一个字都不说,而症状正是真机上那行红字。
func TestNetworkGuardForServeIsFedWhatBxRoutesAround(t *testing.T) {
	opts := controlServeOptions{
		Runtime:    func() RuntimeState { return RuntimeState{ServerBypass: []string{"203.0.113.92/32"}} },
		UserBypass: []string{"10.0.0.0/8"},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g := networkGuardForServe(ctx, opts)
	if g == nil || g.routedAround == nil {
		t.Fatal("the guard was started without knowing what bx routes around")
	}
	want := []netip.Prefix{netip.MustParsePrefix("203.0.113.92/32"), netip.MustParsePrefix("10.0.0.0/8")}
	if got := g.routedAround(); !samePrefixes(got, want) {
		t.Fatalf("guard routed-around = %v, want %v", got, want)
	}
}

func samePrefixes(a, b []netip.Prefix) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func containsPrefix(ps []netip.Prefix, p netip.Prefix) bool {
	for _, q := range ps {
		if q == p {
			return true
		}
	}
	return false
}

// 每次刷新递给平台采集器的必须是守卫自己那份 routedAround —— 这一跳递 nil 全仓
// 照样编译、上面两条照样绿(变异实测),而真机症状就是那行「Outside bx: ssh」回来。
func TestNetworkGuardRefreshHandsItsRoutedAroundToTheCollector(t *testing.T) {
	want := []netip.Prefix{netip.MustParsePrefix("203.0.113.92/32")}
	var received func() []netip.Prefix
	g := &networkGuard{
		routedAround: func() []netip.Prefix { return want },
		collect: func(_ context.Context, routedAround func() []netip.Prefix) []stats.Warning {
			received = routedAround
			return nil
		},
	}
	g.refresh(context.Background())
	if received == nil {
		t.Fatal("the collector was not handed any routed-around function")
	}
	if got := received(); !samePrefixes(got, want) {
		t.Fatalf("collector received routed-around %v, want %v", got, want)
	}
}

// startNetworkGuard 接的采集器必须是生产那份(平台的 collectNetworkWarnings),
// 不然上面那条守的是一个测试自己塞进去的函数。
func TestStartNetworkGuardUsesThePlatformCollector(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g := startNetworkGuard(ctx, nil)
	if g.collect == nil || reflect.ValueOf(g.collect).Pointer() != reflect.ValueOf(collectNetworkWarnings).Pointer() {
		t.Fatal("startNetworkGuard must wire the platform collector")
	}
}
