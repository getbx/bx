package supervisor

import (
	"net/netip"
	"path/filepath"

	"github.com/getbx/bx/internal/pfreset"
	"github.com/getbx/bx/internal/route"
)

type pfResetDecision int

const (
	pfResetSkip pfResetDecision = iota
	pfResetRun
	pfResetDryRun
)

// decidePFReset:空与 on 跑;off 不跑;dry-run 只打印将装的规则与将被重置的连接;
// 认不出的当 off —— 少做只是多漏几分钟(两段式会报),多做是断人连接。
func decidePFReset(mode string) pfResetDecision {
	switch mode {
	case "", "on":
		return pfResetRun
	case "dry-run":
		return pfResetDryRun
	default:
		return pfResetSkip
	}
}

// pfResetPrefixes 是白名单的组装:私网 + 服务器旁路 + 用户 bypass。
func pfResetPrefixes(serverBypass, userBypass []string) []netip.Prefix {
	return pfreset.RoutedAround(route.DefaultPrivateCIDRs, serverBypass, userBypass)
}

// PFTokenPath 是 `pfctl -E` 引用 token 的落盘处(与 core.sock 同目录):Core 崩溃时
// 只有它能告诉 `bx down` 的强制拆除与下一个 Guardian「有一个引用要释放」。
func PFTokenPath() string { return filepath.Join(filepath.Dir(SockPath), "pf.token") }
