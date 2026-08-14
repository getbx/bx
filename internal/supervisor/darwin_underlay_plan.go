package supervisor

import (
	"context"
	"fmt"
	"log"
	"net/netip"
	"sort"
	"strings"
)

// darwinUnderlayCommand is one route command in the recovery-only route plan.
// Fallback is limited to deleting and adding the exact same bypass prefix after
// route change reports that the old exact route is absent.
type darwinUnderlayCommand struct {
	Name          string
	Args          []string
	Fallback      []darwinUnderlayCommand
	IgnoreMissing bool
	// Optional 表示这条命令失败不影响整次恢复。
	//
	// 恢复的首要职责是把流量重新引对;scoped 默认路由只影响 bx **自己**的直连。
	// 让它把恢复整个失败掉,是拿一个次要目标去赌主要目标 —— 与「停止路径不许
	// 依赖别的先成功」同一条纪律。
	Optional bool
}

// darwinUnderlayPlan changes only bx-owned, underlay-dependent IPv4 bypasses.
// It intentionally does not call darwinRouteSpecs: that full-Hijack builder owns
// the capture routes and therefore cannot be part of recovery.
func darwinUnderlayPlan(old, next UnderlaySnapshot, serverBypass, userBypass []string) ([]darwinUnderlayCommand, error) {
	canonicalOld, err := canonicalUnderlaySnapshot(old.Interface, old.Gateway, old.LocalCIDRs)
	if err != nil {
		return nil, fmt.Errorf("invalid previous underlay: %w", err)
	}
	canonicalNext, err := canonicalUnderlaySnapshot(next.Interface, next.Gateway, next.LocalCIDRs)
	if err != nil {
		return nil, fmt.Errorf("invalid next underlay: %w", err)
	}
	if underlayGeneration(canonicalOld) == underlayGeneration(canonicalNext) {
		return nil, nil
	}

	prefixes, err := darwinUnderlayBypassPrefixes(serverBypass, userBypass)
	if err != nil {
		return nil, err
	}
	plan := make([]darwinUnderlayCommand, 0, len(prefixes)+1)
	for _, prefix := range prefixes {
		plan = append(plan, darwinUnderlayChange(prefix, canonicalNext.Gateway.String()))
	}
	// **scoped 默认路由也依赖 underlay,换网后必须重新指向新网关。**
	//
	// 它既不是旁路也不是捕获路由,是第三类东西:让 `IP_BOUND_IF`(bx 自己的
	// 直连/解析/socks 防环用)找得到出口。macOS 只在多网络服务时才自带
	// per-interface scoped default,单服务的机器上没有 —— 见 darwin_routes.go。
	//
	// 不装的后果是真机上验过的:换网之后 `*.qq.com 强制直连 1443 条,失败 1236`,
	// 而保护状态一路显示 Protected。**它不走 darwinUnderlayBypassPrefixes**:
	// 那条路会被 forbiddenDarwinUnderlayPrefix 以「catch-all 不是旁路」挡掉,
	// 而那道守卫是对的 —— 这里要的是显式作用域到某个网卡的一条,不是全局 default。
	if dev := strings.TrimSpace(canonicalNext.Interface); dev != "" {
		plan = append(plan, darwinUnderlayScopedDefault(dev, canonicalOld.Interface, canonicalNext.Gateway.String()))
	}
	return plan, nil
}

// darwinUnderlayScopedDefault 把 scoped 默认路由重新指到新网关。
//
// 先删后加而不是 change:换网时网卡名也可能变(Wi-Fi ↔ 以太网),而 `route change`
// 改不了一条属于**另一个**作用域的路由。旧网卡那条一并清掉,否则它会以一条指向
// 已消失网关的路由留在系统里。
func darwinUnderlayScopedDefault(dev, oldDev, gateway string) darwinUnderlayCommand {
	cleanup := []darwinUnderlayCommand{
		{Name: "route", Args: []string{"-n", "delete", "-ifscope", dev, "default"}, IgnoreMissing: true, Optional: true},
	}
	if oldDev != "" && oldDev != dev {
		cleanup = append(cleanup, darwinUnderlayCommand{
			Name: "route", Args: []string{"-n", "delete", "-ifscope", oldDev, "default"},
			IgnoreMissing: true, Optional: true,
		})
	}
	cleanup = append(cleanup, darwinUnderlayCommand{
		Name: "route", Args: []string{"-n", "add", "-ifscope", dev, "default", gateway}, Optional: true,
	})
	// 外层用一条必然「缺失」的删除引出 Fallback 链,好让执行器按既有语义走完它。
	return darwinUnderlayCommand{
		Name:     "route",
		Args:     []string{"-n", "delete", "-ifscope", dev, "default"},
		Optional: true, IgnoreMissing: true,
		Fallback: cleanup[1:],
	}
}

func darwinUnderlayBypassPrefixes(serverBypass, userBypass []string) ([]string, error) {
	seen := make(map[string]struct{}, len(darwinDirectCIDRs)+len(serverBypass)+len(userBypass))
	add := func(raw string, exact bool) error {
		prefix, err := netip.ParsePrefix(raw)
		if err != nil || prefix != prefix.Masked() || !prefix.Addr().Is4() {
			return fmt.Errorf("invalid IPv4 underlay bypass %q", raw)
		}
		if exact && prefix.Bits() != 32 {
			return fmt.Errorf("server underlay bypass must be an exact IPv4 route: %q", raw)
		}
		if forbiddenDarwinUnderlayPrefix(prefix) {
			return fmt.Errorf("capture or catch-all route is not an underlay bypass: %q", raw)
		}
		seen[prefix.String()] = struct{}{}
		return nil
	}

	for _, prefix := range darwinDirectCIDRs {
		if err := add(prefix, false); err != nil {
			return nil, err
		}
	}
	for _, prefix := range serverBypass {
		if err := add(prefix, true); err != nil {
			return nil, err
		}
	}
	for _, prefix := range userBypass {
		if err := add(prefix, false); err != nil {
			return nil, err
		}
	}

	prefixes := make([]string, 0, len(seen))
	for prefix := range seen {
		prefixes = append(prefixes, prefix)
	}
	sort.Strings(prefixes)
	return prefixes, nil
}

func forbiddenDarwinUnderlayPrefix(prefix netip.Prefix) bool {
	if prefix.Bits() == 0 {
		return true
	}
	for _, capture := range []string{"0.0.0.0/1", "128.0.0.0/1"} {
		if prefix == netip.MustParsePrefix(capture) {
			return true
		}
	}
	return false
}

func darwinUnderlayChange(prefix, gateway string) darwinUnderlayCommand {
	return darwinUnderlayCommand{
		Name: "route",
		Args: []string{"-n", "change", "-net", prefix, gateway},
		Fallback: []darwinUnderlayCommand{
			{
				Name:          "route",
				Args:          []string{"-n", "delete", "-net", prefix},
				IgnoreMissing: true,
			},
			{
				Name: "route",
				Args: []string{"-n", "add", "-net", prefix, gateway},
			},
		},
	}
}

func executeDarwinUnderlayPlan(ctx context.Context, runner commandRunner, plan []darwinUnderlayCommand) error {
	for _, command := range plan {
		err := runner.Run(ctx, command.Name, command.Args...)
		if err == nil {
			// 主命令成功时仍要走完 Fallback:scoped 默认路由那条把「先删后加」
			// 拆成主命令(删)+ Fallback(删旧网卡 / 加新的),两半都必须执行。
			if !command.Optional {
				continue
			}
		} else if !darwinRouteMissing(err) {
			if command.Optional {
				logOptionalUnderlayRoute(command, err)
				continue
			}
			return underlayRebindFailed(command, err)
		}
		for _, fallback := range command.Fallback {
			err := runner.Run(ctx, fallback.Name, fallback.Args...)
			if err == nil || (fallback.IgnoreMissing && darwinRouteMissing(err)) {
				continue
			}
			if fallback.Optional {
				logOptionalUnderlayRoute(fallback, err)
				continue
			}
			return underlayRebindFailed(fallback, err)
		}
	}
	return nil
}

func darwinRouteMissing(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "not in table") || strings.Contains(message, "not found") || strings.Contains(message, "no such process")
}

func underlayRebindFailed(command darwinUnderlayCommand, err error) error {
	return &PathRecoveryError{
		Code:   "underlay_rebind_failed",
		Detail: fmt.Sprintf("%s %s: %v", command.Name, strings.Join(command.Args, " "), err),
	}
}

// logOptionalUnderlayRoute 记下一条可选路由没装上。
//
// **有日志是这条修复能被排查的前提。** 上一版 scoped 默认路由装成没装成一个字
// 都不留(Hijack 用的 runDarwinRouteCommand 成功时不打印),于是真机上「它到底
// 装了没有」在事后完全查不了 —— 排查绕了一大圈,还一度读错了日志文件。
func logOptionalUnderlayRoute(command darwinUnderlayCommand, err error) {
	log.Printf("可选路由未装上(跳过,不影响恢复):%s %s: %v",
		command.Name, strings.Join(command.Args, " "), err)
}
