//go:build darwin

package supervisor

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// scopedEgressProbe 是探测「作用域到物理网卡时,公网出得去吗」用的目的地。
// 用文档地址不合适 —— 它要落在真实的默认路由下,故用一个稳定的公网地址。
const scopedEgressProbe = "8.8.8.8"

// DirectEgressReachable 回答「bx 自己的直连出得去吗」。
//
// **只读**:问内核「作用域到物理网卡时,一个公网地址有没有路由」,不发包也不拨号。
//
// 判据成立的理由:darwin 的 DirectDialer 用 `IP_BOUND_IF` 绑物理网卡防环,而它
// **只查该接口的 scoped 路由表**。macOS 只在多网络服务时才自带 per-interface
// scoped default;单服务的机器上没有,于是所有 direct 规则 ENETUNREACH —— 而
// 隧道毫发无伤(它的 server bypass 恰好是一条显式路由)。2026-08-13 真机上这个
// 故障出现两次,两次都是保护 Protected、四项观测全绿而直连 100% 失败。
//
// 三态:出得去 / 出不去 / 问不出来。**「问不出来」绝不压成「出不去」** ——
// 探不到物理网卡名时我们对这件事一无所知,报 False 会让每台机器恒红。
func DirectEgressReachable(ctx context.Context) (reachable bool, known bool, err error) {
	_, dev, devErr := defaultRouteDarwinContext(ctx)
	var iface string
	var lookupErr error
	if devErr == nil && strings.TrimSpace(dev) != "" {
		iface, lookupErr = lookupScopedRouteDarwin(ctx, dev, scopedEgressProbe)
	}
	return decideDirectEgress(dev, devErr, iface, lookupErr)
}

// decideDirectEgress 是这项观测的**全部判定**,抽出来是因为上面那层直接 exec、
// 测试进不去 —— 而这个仓库全部的事故都在那种进不去的地方。
//
// 三态,而且「问不出来」绝不压成「出不去」:后者是一个确定的坏消息,会让每台
// 机器在 route 抽风时恒红。
func decideDirectEgress(dev string, devErr error, iface string, lookupErr error) (reachable bool, known bool, err error) {
	if devErr != nil {
		return false, false, devErr
	}
	dev = strings.TrimSpace(dev)
	if dev == "" {
		// 探不到物理网卡名 —— 对这件事一无所知。
		return false, false, nil
	}
	if lookupErr != nil {
		// `not in table` 是**答案**(scoped 表里没有出口),不是查询失败;
		// 别的错误(超时、权限、命令缺失)一律是观测失败。
		if isScopedRouteMissing(lookupErr) {
			return false, true, nil
		}
		return false, false, lookupErr
	}
	// 路由存在,但必须确实落在那块物理网卡上;落在别处说明作用域没生效。
	return strings.EqualFold(strings.TrimSpace(iface), dev), true, nil
}

// lookupScopedRouteDarwin 问「作用域到 dev 时,发往 destination 的包走哪个接口」。
//
// `route -n get -ifscope <dev> <ip>`:scoped 表里没有匹配时它以非零退出并在
// stderr 打 `not in table` —— 那是一个**答案**(出不去),不是查询失败。
func lookupScopedRouteDarwin(ctx context.Context, dev, destination string) (string, error) {
	out, err := exec.CommandContext(ctx, "route", "-n", "get", "-ifscope", dev, destination).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	selection, perr := parseDarwinRouteSelection(out)
	if perr != nil {
		return "", perr
	}
	return selection.Interface, nil
}

// isScopedRouteMissing 判断错误是不是「scoped 表里没有这条」。
//
// **不认得的错误不许当作「没有」**:那会把一次查询失败伪装成一个确定的坏消息,
// 与本仓库对 Tristate 的纪律相反。
func isScopedRouteMissing(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "not in table")
}
