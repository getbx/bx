//go:build windows

package supervisor

import (
	"context"
	"errors"
	"fmt"
)

// ErrRouteMissing 在本平台不会返回;导出只为调用方编译。
var ErrRouteMissing = errors.New("route not in table")

var errBoundRouteUnsupported = errors.New("bound route lookup is only implemented on darwin and linux")

// LookupBoundRoute 在本平台没有原语:如实报「不支持」,调用方按「没问」处理。
//
// 它问的是「一个**绑了网卡**的 socket 会走哪条路」(darwin 的 `-ifscope`、
// linux 的 `oif`)。Windows 上 bx 的防环走 `IP_UNICAST_IF` 而不是 scoped
// 路由表,没有对应的查询,**不许拿普通路由查询冒充它** —— 那会把
// 「绑网卡之后走哪儿」答成「不绑走哪儿」,而这两者不同正是 2026-08-13 那次
// 事故的全部内容。
func LookupBoundRoute(context.Context, string, string) (RouteSelection, error) {
	return RouteSelection{}, errBoundRouteUnsupported
}

// PhysicalDefaultRoute 是物理默认路由的网关与网卡名。
//
// **此前 Windows 落在 `!darwin && !linux` 那份兜底上,恒报「不支持」** ——
// 后果是 `bx explain` 在 Windows 上永远说不出「这块是物理网卡」:
// pathview 的接口归属靠 `Facts.PhysicalDev` 对上名字,而那一格一直是空的。
// 2026-09-15 真机实测(`030-SJWJ-GSR-B`)的原话是
// 「普通程序连它会走 WLAN,**我认不出这个接口是什么**」—— 而那台机器上
// WLAN 就是物理 Wi-Fi,bx 自己的 Hijack 每次都正确地挑中它。
//
// 判据**复用 Hijack 用的那一份** `physicalDefaultRoute()`(metric 感知:
// Windows 的有效 metric = 路由 metric + 接口 metric,只比前者会在多网卡上
// 错选 —— 那条教训写在它自己的注释里),这里只多做一步 LUID → 网卡名。
// 第二份实现会与 Hijack 漂开,而漂开的后果是 explain 说的那块网卡与 bx
// 真正用的那块不是同一块。
func PhysicalDefaultRoute(context.Context) (gateway, device string, err error) {
	gw, luid, err := physicalDefaultRoute()
	if err != nil {
		return "", "", err
	}
	row, err := luid.Interface()
	if err != nil {
		// **网关问出来了、名字没问出来:如实报错,不返回半份答案。**
		// 调用方按「没问出来」处理;给一个空的 device 会让 pathview 拿空串
		// 去比对接口名,而空串永远对不上 —— 与「认不出」在输出上一样,
		// 却少了一条可查的错误。
		return "", "", fmt.Errorf("查物理默认路由的网卡名(LUID %v): %w", luid, err)
	}
	return gw.String(), row.Alias(), nil
}
