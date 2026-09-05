//go:build !darwin

package supervisor

import (
	"context"
	"net/netip"
)

// ServerBypassRoutesIntact 在非 darwin 平台暂无原语:恒「问不出来」,循环据此不动。
// Linux 的旁路走策略路由(table 100 之外的 pref-150 route),网卡重连是否冲掉它
// 尚未在真机观察过;要接的时候照 darwin 那份的形状供货。
func ServerBypassRoutesIntact(context.Context, string, func() []netip.Addr) (bool, bool, error) {
	return false, false, nil
}
