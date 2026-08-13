//go:build linux

package supervisor

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"time"
)

// RouteSelection 是一次路由查询的结果:内核会把发往该目的地的包交给谁。
type RouteSelection struct {
	Gateway   string
	Interface string
	Reject    bool
}

// ErrNoRouteToDestination:**确知**没有到达该目的地的路由,与「问不出来」分开。
var ErrNoRouteToDestination = errors.New("no route to destination")

// LookupRoute 问内核「发往 destination 的包现在走哪里」。
//
// 这是观测的基石:它对「谁装的这条路由」完全不关心,因此天然免疫所有权簿记问题。
//
// **必须用 `ip route get`,不能读 main 表。** bx 在 Linux 上靠 ip rule 策略路由分流
// (table 100 + pref 200),而 `ip route show` 默认只列 main —— 读它会在 bx 正常工作时
// 报出物理网卡,把一台受保护的机器说成没受保护。
func LookupRoute(parent context.Context, destination string, ipv6 bool) (RouteSelection, error) {
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()
	args := []string{"route", "get", destination}
	if ipv6 {
		args = append([]string{"-6"}, args...)
	}
	out, err := exec.CommandContext(ctx, "ip", args...).CombinedOutput()
	return parseIPRouteGet(string(out), err)
}

// parseIPRouteGet 把 `ip route get` 的输出翻成一次路由查询的结果。
//
// **提成纯函数是为了能测。** 留在 exec 那一侧时,标准套件只会查一个正常可达的地址,
// 于是 Reject 与「没拿到接口」两条路径**零覆盖** —— 实测:把它们改坏,整套测试全绿。
//
// 三种输出按真实行为区分(2026-08-12 容器实测):
//
//	1.1.1.1 via 172.17.0.1 dev eth0  src 172.17.0.4   → 正常
//	ip: RTNETLINK answers: No route to host           → 命中 unreachable 路由
//	ip: RTNETLINK answers: Network is unreachable     → 压根没有路由
//
// **中间那条正是 bx 自己 v6 fail-closed 屏障的形状**,与最后一条混为一谈就分不清
// 「这台机器没有 v6」与「bx 正在挡住 v6」:两者都不漏,但只有后者会在 bx 关掉之后
// 变回可漏,而 observe 的 barrier_present 恰恰要靠这个区别。
func parseIPRouteGet(text string, runErr error) (RouteSelection, error) {
	switch {
	case strings.Contains(text, "No route to host"):
		return RouteSelection{Reject: true}, nil
	case strings.Contains(text, "Network is unreachable"):
		return RouteSelection{}, ErrNoRouteToDestination
	}
	if runErr != nil {
		return RouteSelection{}, runErr
	}

	selection := RouteSelection{}
	fields := strings.Fields(text)
	for i := 0; i+1 < len(fields); i++ {
		switch fields[i] {
		case "dev":
			if selection.Interface == "" {
				selection.Interface = fields[i+1]
			}
		case "via":
			if selection.Gateway == "" {
				selection.Gateway = fields[i+1]
			}
		}
	}
	if selection.Interface == "" {
		// **零值不许冒充「查到了」。**
		return RouteSelection{}, errors.New("ip route get returned no device: " + strings.TrimSpace(text))
	}
	return selection, nil
}
