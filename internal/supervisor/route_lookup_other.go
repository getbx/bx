//go:build !darwin && !linux && !windows

package supervisor

import (
	"context"
	"errors"
)

// RouteSelection 是一次路由查询的结果:内核会把发往该目的地的包交给谁。
type RouteSelection struct {
	Gateway   string
	Interface string
	Reject    bool
}

var errRouteLookupUnsupported = errors.New("route lookup is only implemented on darwin")

// LookupRoute 在非 darwin 平台返回明确的不支持错误,绝不返回零值冒充查询成功。
func LookupRoute(context.Context, string, bool) (RouteSelection, error) {
	return RouteSelection{}, errRouteLookupUnsupported
}
