package guardian

import (
	"net/netip"
	"strings"
	"sync"

	"github.com/getbx/bx/internal/embedded"
	"github.com/getbx/bx/internal/route"
)

// chinaResolverOnce 只解析一次内嵌的 china CIDR 列表。它有几千条,而状态查询在
// 菜单打开时是每两秒一次 —— 每次重建就是拿一行装饰去换持续的 CPU。
var chinaResolverOnce struct {
	sync.Once
	set *route.CIDRSet
}

// chinaResolverPredicate 返回「这个 IP 在不在中国大陆」的判据,**问不出来时返回
// nil**(而不是一个恒 false 的函数)。
//
// 这个区别是承重的:恒 false 会让每一个中国大陆的解析器都悄悄失去国旗,而调用方
// 无从分辨「证明了不在中国」与「压根没能证明」——正是 tristate 那条纪律。
func chinaResolverPredicate() func(netip.Addr) bool {
	chinaResolverOnce.Do(func() {
		raw := embedded.ChinaCIDR()
		if len(raw) == 0 {
			return
		}
		set, err := route.NewCIDRSet(strings.Split(string(raw), "\n"))
		if err != nil {
			return
		}
		chinaResolverOnce.set = set
	})
	if chinaResolverOnce.set == nil {
		return nil
	}
	return chinaResolverOnce.set.Contains
}
