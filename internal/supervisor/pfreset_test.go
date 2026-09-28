package supervisor

import (
	"net/netip"
	"testing"
)

// 白名单必须同时装着私网、服务器旁路、用户 bypass —— 少任何一份,重置就会打到
// bx 自己安排走物理网卡的连接(到自己 VPS 的 ssh、局域网)。
func TestPFResetPrefixesCarryPrivateServerAndUserBypass(t *testing.T) {
	got := pfResetPrefixes([]string{"203.0.113.92/32"}, []string{"198.51.100.7"})
	for _, want := range []string{"10.0.0.0/8", "192.168.0.0/16", "203.0.113.92/32", "198.51.100.7/32"} {
		p := netip.MustParsePrefix(want)
		found := false
		for _, g := range got {
			if g == p {
				found = true
			}
		}
		if !found {
			t.Fatalf("routed-around %v misses %s", got, want)
		}
	}
}

// 三态:空/on 跑,off 不跑,dry-run 只看不装;认不出的当 off(少做不会漏 IP,多做会断连接)。
func TestPFResetModeDecidesWhetherToTouchPF(t *testing.T) {
	for mode, want := range map[string]pfResetDecision{"": pfResetRun, "on": pfResetRun, "off": pfResetSkip, "dry-run": pfResetDryRun, "bogus": pfResetSkip} {
		if got := decidePFReset(mode); got != want {
			t.Fatalf("mode %q → %v, want %v", mode, got, want)
		}
	}
}
