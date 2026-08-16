//go:build darwin

package supervisor

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// liveEgressRepair 重装那条 scoped 默认路由。
//
// **每次现问网关与网卡名,不用启动时那份记账** —— 这整件事的起因就是记账与
// 内核对不上;拿一份可能已经陈旧的快照去装路由,等于把同一个错误再犯一次。
//
// **只加不删。** 多服务的 Mac 上系统自带一条同款,`route add` 会以
// "File exists" 失败 —— 那是好消息(路由在),不是错误。而删它会打断用户的
// 网络,且 bx 无从恢复(2026-08-13 那条 optional 路由的教训)。
func liveEgressRepair(ctx context.Context) error {
	gw, dev, err := defaultRouteDarwinContext(ctx)
	if err != nil {
		return fmt.Errorf("探测默认路由: %w", err)
	}
	gw, dev = strings.TrimSpace(gw), strings.TrimSpace(dev)
	if gw == "" || dev == "" {
		return fmt.Errorf("默认路由缺网关或网卡(gw=%q dev=%q)", gw, dev)
	}
	out, err := exec.CommandContext(ctx, "route", "-n", "add", "-ifscope", dev, "default", gw).CombinedOutput()
	return egressRepairOutcome(err, string(out), dev, gw)
}
