//go:build !darwin && !linux && !windows

package supervisor

import (
	"context"
	"errors"
)

// ErrRouteMissing 在本平台不会返回;导出只为调用方编译。
var ErrRouteMissing = errors.New("route not in table")

var errBoundRouteUnsupported = errors.New("bound route lookup is only implemented on darwin and linux")

// LookupBoundRoute 在本平台没有原语:如实报「不支持」,调用方按「没问」处理。
func LookupBoundRoute(context.Context, string, string) (RouteSelection, error) {
	return RouteSelection{}, errBoundRouteUnsupported
}

// PhysicalDefaultRoute 同上。
func PhysicalDefaultRoute(context.Context) (string, string, error) {
	return "", "", errBoundRouteUnsupported
}
