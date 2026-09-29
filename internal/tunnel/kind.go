package tunnel

import "github.com/getbx/bx/internal/linkkind"

// 传输种类名与「scheme → 引擎」这张表住在 internal/linkkind(叶子包,不起进程);
// 这里是薄壳,让既有调用方(supervisor、setup、cli……)一字不改。
const (
	KindReality     = linkkind.KindReality
	KindHysteria2   = linkkind.KindHysteria2
	KindTrojan      = linkkind.KindTrojan
	KindShadowsocks = linkkind.KindShadowsocks
	KindVmess       = linkkind.KindVmess
	KindBrook       = linkkind.KindBrook
)

// Kind 由 server link 的 scheme 选传输引擎。判据在 linkkind.Kind。
func Kind(link string) string { return linkkind.Kind(link) }

// Kinds 返回 Kind 可能返回的全部值。判据在 linkkind.Kinds。
func Kinds() []string { return linkkind.Kinds() }

// IsClientLink 报告 link 是否是受支持的裸客户端传输链接。判据在 linkkind.IsClientLink。
func IsClientLink(link string) bool { return linkkind.IsClientLink(link) }
