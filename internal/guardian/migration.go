package guardian

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
)

// MigrationRequest is deliberately limited to non-secret route handoff data.
type MigrationRequest struct {
	Gateway      string   `json:"gateway"`
	ServerBypass []string `json:"server_bypass"`
}

func ValidateMigrationRequest(request MigrationRequest) (MigrationRequest, error) {
	gateway, err := parseIPv4(request.Gateway)
	if err != nil {
		return MigrationRequest{}, fmt.Errorf("invalid migration gateway: %w", err)
	}
	if len(request.ServerBypass) == 0 {
		return MigrationRequest{}, errors.New("migration server bypass required")
	}

	normalized := MigrationRequest{Gateway: gateway.String()}
	seen := make(map[string]struct{}, len(request.ServerBypass))
	hasIPv4 := false
	for _, value := range request.ServerBypass {
		prefix, err := netip.ParsePrefix(value)
		if err != nil || prefix != prefix.Masked() {
			return MigrationRequest{}, fmt.Errorf("migration bypass must be an exact IP prefix: %q", value)
		}
		addr := prefix.Addr().Unmap()
		switch {
		case addr.Is4() && prefix.Bits() == 32:
			prefix = netip.PrefixFrom(addr, 32)
			hasIPv4 = true
		case addr.Is6() && prefix.Bits() == 128:
			prefix = netip.PrefixFrom(addr, 128)
		default:
			return MigrationRequest{}, fmt.Errorf("migration bypass must be an exact IPv4 /32 or IPv6 /128: %q", value)
		}
		if addr.IsUnspecified() || addr.IsMulticast() {
			return MigrationRequest{}, fmt.Errorf("migration bypass is not a usable server address: %q", value)
		}
		canonical := prefix.String()
		if _, duplicate := seen[canonical]; duplicate {
			continue
		}
		seen[canonical] = struct{}{}
		normalized.ServerBypass = append(normalized.ServerBypass, canonical)
	}
	if !hasIPv4 {
		return MigrationRequest{}, errors.New("migration requires at least one IPv4 /32 server bypass")
	}
	return normalized, nil
}

func migrationBarrierContext(request MigrationRequest) BarrierContext {
	bypasses := make([]string, 0, len(request.ServerBypass))
	for _, value := range request.ServerBypass {
		prefix, err := netip.ParsePrefix(value)
		if err == nil && prefix.Addr().Is4() && prefix.Bits() == 32 {
			bypasses = append(bypasses, prefix.String())
		}
	}
	return BarrierContext{Gateway: request.Gateway, ServerBypass: bypasses, BlockIPv6: true}
}

// InstallHandoffBarrier 由 CLI 在 Guardian 之外装上与 Manager.Migrate **同一份**屏障计划
// (migrationBarrierContext):服务器 /32 经物理网关、其余公网一律 reject。
//
// 用途只有一个:切换 Guardian 自己(D3,docs/superpowers/specs/2026-09-25-fail-closed-guardian-switch-design.md)。
// 旧 Guardian 的 launchd 任务没有 AbandonProcessGroup,bootout 它会顺带收掉 Core,Core 还原
// 路由 —— 没有屏障的话从那一刻起流量从物理网卡直出。屏障先装,那几秒就是断网而不是泄漏。
// 新 Guardian 起来后经 /v1/migrate 用同一份计划接过它(`file exists` 被容忍),再按自己的
// 所有权记录释放。
func InstallHandoffBarrier(ctx context.Context, request MigrationRequest) error {
	normalized, err := ValidateMigrationRequest(request)
	if err != nil {
		return err
	}
	return NewBarrier(nil).Install(ctx, migrationBarrierContext(normalized))
}

// ReassertHandoffBypass 把屏障里的服务器 /32 旁路补回来:旧 Core 退出时会删掉它自己
// 装的那条同名路由,而新 Core 的隧道正是要经它出去。
func ReassertHandoffBypass(ctx context.Context, request MigrationRequest) error {
	normalized, err := ValidateMigrationRequest(request)
	if err != nil {
		return err
	}
	return NewBarrier(nil).ReassertBypass(ctx, migrationBarrierContext(normalized))
}
