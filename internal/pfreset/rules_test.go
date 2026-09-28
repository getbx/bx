package pfreset

import (
	"net/netip"
	"strings"
	"testing"
)

// 规则文本是 pf 真正吃的东西,逐句钉:网卡名来自参数(热点是 en0、有线可能是 en5)、
// 白名单表里每一段都在、TCP 回 RST、UDP 回 ICMP、user != root、quick。
func TestRulesNameTheDeviceAndEveryRoutedAroundPrefix(t *testing.T) {
	around := []netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("203.0.113.92/32"),
	}
	got := Rules("en5", around)
	for _, want := range []string{
		"table <bx_routed_around> persist { 10.0.0.0/8, 203.0.113.92/32 }",
		"block return-rst out quick on en5 inet proto tcp from (en5) to !<bx_routed_around> user != root",
		"block return-icmp out quick on en5 inet proto udp from (en5) to !<bx_routed_around> user != root",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("rules miss %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "en0") {
		t.Fatalf("the device must come from the caller, got a hardcoded en0:\n%s", got)
	}
}

// 白名单 = 私网 + 服务器旁路 + 用户 bypass;裸 IP 补 /32,认不出的丢掉不编,去重。
func TestRoutedAroundJoinsPrivateServerAndUserBypass(t *testing.T) {
	// 203.0.113.5/24 带主机位:pf 的表拒收这种条目,整份规则会装不上 —— 先 Masked。
	got := RoutedAround([]string{"10.0.0.0/8", "192.168.0.0/16"}, []string{"203.0.113.92/32"}, []string{"198.51.100.7", "garbage", "10.0.0.0/8", "203.0.113.5/24"})
	want := []string{"10.0.0.0/8", "192.168.0.0/16", "203.0.113.92/32", "198.51.100.7/32", "203.0.113.0/24"}
	if len(got) != len(want) {
		t.Fatalf("routed-around = %v, want %v", got, want)
	}
	for i := range want {
		if got[i].String() != want[i] {
			t.Fatalf("routed-around[%d] = %s, want %s", i, got[i], want[i])
		}
	}
}
