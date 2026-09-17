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

// lookupRouteSupported 为假:本平台没有 LookupRoute 的实现。
//
// 契约是「查不到要**说出来**」—— LookupRoute 必须明确报错,不得返回零值冒充
// 查到了。守卫按这个常量选分支,不按手抄的 GOOS 清单(见各实现文件里那份注释)。
const lookupRouteSupported = false
