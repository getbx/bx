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
