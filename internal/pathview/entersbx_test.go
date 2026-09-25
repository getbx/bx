package pathview

import (
	"net/netip"
	"testing"

	"github.com/getbx/bx/internal/tristate"
)

// —— 「这个目标的包会不会进 bx」必须是个可以回答「不知道」的问题 ——
//
// 起因是真机上一次自相矛盾的输出(2026-09-16,项目所有者的公司内网):
//
//	结论      普通程序连它会直接从 en0 出去,不经过 bx
//	本机路由  en0 via 10.84.6.1(物理网卡)
//	...
//	TCP       TUNNEL
//	  依据    没有命中任何列表(默认)
//
// 两半都对,只是在回答**两个不同的问题**:上半截是「这台机器实际会怎么走」,
// 下半截是「**如果**一条到这个域名的连接进了 bx,bx 会怎么判」。而私网目的地
// 的包根本进不了 TUN(bx 自己把 10/8 旁路到物理网关),所以下半截描述的那件事
// 永远不会发生 —— 用户没有义务知道该信哪一个。
//
// 判据放在这里(纯判据包)而不是渲染层:渲染层已经有过一次「界面悄悄替服务端
// 说了一句它没说过的话」的账。
func TestViewSaysWhetherTrafficEntersBx(t *testing.T) {
	base := func(iface string) Facts {
		return Facts{
			Addrs:       []netip.Addr{netip.MustParseAddr("10.0.13.23")},
			Route:       RouteFact{Applicable: true, Interface: iface},
			BxTun:       "utun8",
			BxTunKnown:  true,
			PhysicalDev: "en0",
		}
	}

	for _, tc := range []struct {
		name  string
		facts Facts
		want  tristate.Tristate
	}{
		{"走 bx 自己的 TUN", base("utun8"), tristate.True},
		{"走物理网卡", base("en0"), tristate.False},
		// 别人的隧道同样**不经过 bx** —— 这一格答 False 不是「不知道」:
		// 我们认出了那个接口,而它不是 bx 的。
		{"走别人的隧道", base("utun3"), tristate.False},
		// 认不出的接口:**绝不许塌成任一边**。说 True 会把一条其实没进 bx 的
		// 流量说成 bx 在管;说 False 会把 bx 正在管的说成没管。
		{"认不出的接口", base("weird9"), tristate.Unknown},
		{"根本没问出路由", Facts{Addrs: []netip.Addr{netip.MustParseAddr("10.0.13.23")}}, tristate.Unknown},
		func() struct {
			name  string
			facts Facts
			want  tristate.Tristate
		} {
			// bx 的 TUN 名字问不出来时,哪怕接口串恰好长得像也不能认:
			// BxTunKnown 说的是「问出来了」,不是「有」。
			f := base("utun8")
			f.BxTunKnown = false
			return struct {
				name  string
				facts Facts
				want  tristate.Tristate
			}{"没问出 bx 的 TUN 名", f, tristate.Unknown}
		}(),
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Judge(tc.facts).EntersBx; got != tc.want {
				t.Errorf("EntersBx = %v,want %v", got, tc.want)
			}
		})
	}
}

// **零值是 Unknown。** 与 observe.Tristate、leakcheck.ReachUndetermined、rulereview 的 Class
// 同一条:漏填时多报好过漏报,而这里「漏报」的具体形状是让一条永远不会发生的
// 判定继续冒充实际行为。
func TestEntersBxZeroValueIsUnknown(t *testing.T) {
	var v View
	if v.EntersBx != tristate.Unknown {
		t.Fatalf("View 零值的 EntersBx = %v —— 它会被读成一个确定的答案", v.EntersBx)
	}
}
