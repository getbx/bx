//go:build darwin

package supervisor

import "context"

// RouteSelection 是一次路由查询的结果:内核会把发往该目的地的包交给谁。
type RouteSelection struct {
	Gateway   string
	Interface string
	Reject    bool
}

// LookupRoute 问内核"发往 destination 的包现在走哪里"。
//
// 这是观测的基石:它对"谁装的这条路由"完全不关心,因此天然免疫所有权簿记问题。
func LookupRoute(ctx context.Context, destination string, ipv6 bool) (RouteSelection, error) {
	selection, err := darwinRouteLookup(ctx, destination, ipv6)
	if err != nil {
		return RouteSelection{}, err
	}
	return RouteSelection{
		Gateway:   selection.Gateway,
		Interface: selection.Interface,
		Reject:    selection.Reject,
	}, nil
}

// lookupRouteSupported:本平台有 LookupRoute 的实现。
//
// 它取代了守卫里那份**手抄的 GOOS 清单**。清单会漂,而且已经漂过一次:
// route_lookup_windows.go 落地之后,清单仍写着 darwin/linux,于是 windows 上
// 那条守卫去断言「必须报错」,而实现好好地答了出来 —— 测试红在一条与缺陷
// 完全无关的地方。常量跟着实现文件走,漏写它连编译都过不去。
const lookupRouteSupported = true
