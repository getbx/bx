package guardian

import (
	"net/netip"
	"strings"
	"testing"
)

func inChinaFor(prefixes ...string) func(netip.Addr) bool {
	return func(ip netip.Addr) bool {
		for _, raw := range prefixes {
			if p, err := netip.ParsePrefix(raw); err == nil && p.Contains(ip) {
				return true
			}
		}
		return false
	}
}

// 直连解析器那一行要回答的是「我的直连域名查询交给了谁」。
//
// **国旗只在能证明的时候出现。** bx 不带 geoip 库,所以它能离线证明的只有
// 「在不在中国大陆」—— 靠它自己已经内嵌的 china CIDR 列表。别的国家一律不给旗:
// 猜一个国旗出来,与判据编一个大区去比是同一种错,而且更显眼。
//
// **anycast 让「国家」这个问题本身就没有答案**:1.1.1.1 / 8.8.8.8 在每个大洲都
// 有落地,给它挂任何一面旗都是假的。
func TestResolverLabel(t *testing.T) {
	china := inChinaFor("223.5.5.0/24", "114.114.114.0/24")

	for _, tc := range []struct {
		name       string
		addr       string
		want       string
		wantNoFlag bool
	}{
		{name: "中国大陆的公共解析器,可证明", addr: "223.5.5.5", want: "223.5.5.5 🇨🇳"},
		{name: "另一个中国大陆解析器", addr: "114.114.114.114", want: "114.114.114.114 🇨🇳"},
		{
			// anycast:每个大洲都有落地,给它挂旗就是假的。
			name: "境外/anycast 不给国旗", addr: "1.1.1.1", want: "1.1.1.1", wantNoFlag: true,
		},
		{
			// 局域网地址:查询交给了这台路由器,而它交给了谁 bx 不知道 ——
			// 这句话本身就是用户要的信息。
			name: "局域网地址点名说它是路由器", addr: "192.168.1.1",
			want: "192.168.1.1 (your router)", wantNoFlag: true,
		},
		{name: "回环是 bx 自己", addr: "127.0.0.1", want: "127.0.0.1 (bx)", wantNoFlag: true},
		{name: "带端口也认得", addr: "223.5.5.5:53", want: "223.5.5.5 🇨🇳"},
		{name: "空值原样为空", addr: "", want: ""},
		{
			// **解析不出来就原样带出去,不猜。** 一个认不出的串仍然是用户配的东西,
			// 显示它比吞掉它有用;而给它加任何判断都是编造。
			name: "认不出的串原样返回", addr: "dns.example.com", want: "dns.example.com", wantNoFlag: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolverLabel(tc.addr, china)
			if got != tc.want {
				t.Fatalf("ResolverLabel(%q) = %q, want %q", tc.addr, got, tc.want)
			}
			if tc.wantNoFlag && strings.Contains(got, "🇨🇳") {
				t.Errorf("%q 不该带中国国旗:%q", tc.addr, got)
			}
		})
	}
}

// **判不出「在不在中国」时不许给旗。** 列表没加载出来时 inChina 为 nil,
// 那是「没问出来」,而不是「不在中国」—— 与这个仓库其余各处同一条纪律。
func TestResolverLabelGivesNoFlagWhenChinaListIsUnavailable(t *testing.T) {
	got := ResolverLabel("223.5.5.5", nil)
	if got != "223.5.5.5" {
		t.Fatalf("列表不可用时 = %q,want 纯地址(不许猜旗)", got)
	}
}
