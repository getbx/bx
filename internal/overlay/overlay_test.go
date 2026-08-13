package overlay

import (
	"net/netip"
	"strings"
	"testing"
)

func addrs(list ...string) []netip.Addr {
	var out []netip.Addr
	for _, s := range list {
		out = append(out, netip.MustParseAddr(s))
	}
	return out
}

func names(present []Tenant) []string {
	var out []string
	for _, t := range present {
		out = append(out, t.Name)
	}
	return out
}

// **两种信号的可用性随平台变,所以命中任一就算在跑。**
//
// Tailscale 在 Linux 上叫 tailscale0,在 macOS 上却是 utunN —— 与任何别的隧道
// 同名,那里只能靠 CGNAT 地址认。ZeroTier 反过来:地址是 RFC1918、与局域网无法
// 区分,只能靠接口名。要求两种信号都命中,等于在每个平台上都认不出至少一个租户。
func TestDetectAcceptsEitherSignal(t *testing.T) {
	for _, tc := range []struct {
		name    string
		signals Signals
		want    []string
	}{
		{
			name:    "linux 上的 tailscale:接口名认得出",
			signals: Signals{InterfaceNames: []string{"lo", "eth0", "tailscale0"}},
			want:    []string{"tailscale"},
		},
		{
			name: "macOS 上的 tailscale:接口名是 utun,只能靠 CGNAT 地址",
			signals: Signals{
				InterfaceNames: []string{"lo0", "en0", "utun4"},
				Addresses:      addrs("192.168.1.20", "100.101.102.103"),
			},
			want: []string{"tailscale"},
		},
		{
			name:    "zerotier:只有接口名可用(地址是 RFC1918,认不出)",
			signals: Signals{InterfaceNames: []string{"en0", "ztabcdef12"}, Addresses: addrs("10.147.20.5")},
			want:    []string{"zerotier"},
		},
		{
			name: "两个一起跑",
			signals: Signals{
				InterfaceNames: []string{"tailscale0", "ztabcdef12"},
				Addresses:      addrs("100.64.1.1"),
			},
			want: []string{"tailscale", "zerotier"},
		},
		{
			name:    "干净机器:什么都不该认出来",
			signals: Signals{InterfaceNames: []string{"lo0", "en0", "bridge0"}, Addresses: addrs("192.168.1.20")},
			want:    nil,
		},
		{
			// **别的隧道不该被认成任何租户。** 认错的代价是给几条陌生 /32 开旁路,
			// 以及把查询送给一个根本不存在的解析器。
			name:    "别人的 OpenVPN/WireGuard 不是租户",
			signals: Signals{InterfaceNames: []string{"tun0", "wg0", "utun3"}, Addresses: addrs("10.8.0.6")},
			want:    nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := names(Detect(tc.signals))
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("Detect = %v, want %v", got, tc.want)
			}
		})
	}
}

// **没在跑的租户一条旁路、一条 split 都不许出。**
//
// 写死的公网 IP 是泄漏面:地址被回收给别人之后那条旁路仍在,发往陌生人的流量
// 绕过隧道。按在跑与否门控,把影响面限制在真正需要它的机器上。
// split 同理 —— 解析器是租户自己起的进程,它没跑时把查询送过去只会挂住。
func TestNothingIsAppliedForTenantsThatAreNotRunning(t *testing.T) {
	none := Detect(Signals{InterfaceNames: []string{"en0"}})
	if got := BypassCIDRs(none); len(got) != 0 {
		t.Errorf("没有租户在跑却出了旁路 %v —— 写死的公网 IP 是泄漏面", got)
	}
	if got := SplitRoutes(none); len(got) != 0 {
		t.Errorf("没有租户在跑却出了 split %v —— 查询会被送给一个没起来的解析器", got)
	}
	if got := DirectCIDRs(none); len(got) != 0 {
		t.Errorf("没有租户在跑却出了直连段 %v", got)
	}
}

// Tailscale 在跑时:CGNAT 要额外直连,ts.net 要交给 MagicDNS。
//
// **CGNAT 必须显式声明**,因为它不在 RFC1918 —— 通用私网规则盖不住它。
func TestTailscaleGetsItsAddressSpaceAndNamespace(t *testing.T) {
	present := Detect(Signals{Addresses: addrs("100.64.1.1")})
	if got := DirectCIDRs(present); len(got) != 1 || got[0] != "100.64.0.0/10" {
		t.Fatalf("DirectCIDRs = %v,want CGNAT —— 它不在 RFC1918,通用规则盖不住", got)
	}
	routes := SplitRoutes(present)
	if len(routes) != 1 || routes[0].Suffix != "ts.net" || routes[0].Resolver != "100.100.100.100" {
		t.Fatalf("SplitRoutes = %+v,want ts.net → 100.100.100.100", routes)
	}
}

// **ZeroTier 不声明 DirectCIDRs,这是信息不是遗漏。**
//
// 它默认发 RFC1918 地址,而那些段本来就在 route.DefaultPrivateCIDRs 里恒直连 ——
// 它不需要特例,恰恰因为它守规矩。谁哪天给它补上一段 RFC1918,那是重复而不是修复。
func TestZeroTierNeedsNoExtraDirectCIDRsBecauseItUsesRFC1918(t *testing.T) {
	present := Detect(Signals{InterfaceNames: []string{"ztabcdef12"}})
	if got := DirectCIDRs(present); len(got) != 0 {
		t.Errorf("ZeroTier 出了额外直连段 %v —— 它用 RFC1918,已被通用私网规则覆盖", got)
	}
	// 但它的根节点是**公网** IP,那一半今天完全没有旁路,是这次要补的。
	if got := BypassCIDRs(present); len(got) == 0 {
		t.Error("ZeroTier 在跑却没有根节点旁路 —— 隧道 fail-closed 时它连根都连不上")
	}
	if got := RelayHosts(present); len(got) == 0 {
		t.Error("必须声明根节点主机名 —— 上游明说 IP 会变,主机名才是权威来源")
	}
}

// 表里每一行都要能被至少一种信号认出来,否则那一行是死的。
func TestEveryTenantIsDetectableBySomething(t *testing.T) {
	for _, tenant := range Tenants() {
		if len(tenant.IfacePrefixes) == 0 && !tenant.OverlayPrefix.IsValid() {
			t.Errorf("租户 %s 没有任何检测信号 —— 它永远不会被认出来,整行是死的", tenant.Name)
		}
		if tenant.Resolver != "" && len(tenant.DNSSuffixes) == 0 {
			t.Errorf("租户 %s 声明了解析器却没有后缀 —— 那个解析器永远不会被用到", tenant.Name)
		}
		if tenant.Resolver == "" && len(tenant.DNSSuffixes) > 0 {
			t.Errorf("租户 %s 声明了后缀却没有解析器 —— 那些查询无处可去", tenant.Name)
		}
		if len(tenant.RelayFallbackCIDRs) > 0 && len(tenant.RelayHosts) == 0 {
			t.Errorf("租户 %s 只有兜底 IP 没有主机名 —— 那些 IP 会变,而没有权威来源可以纠正它们",
				tenant.Name)
		}
	}
}
