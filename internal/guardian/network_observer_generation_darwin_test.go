package guardian

import (
	"net"
	"net/netip"
	"testing"

	"github.com/getbx/bx/internal/supervisor"
)

func ipNet(t *testing.T, cidr string) net.Addr {
	t.Helper()
	prefix := netip.MustParsePrefix(cidr)
	addr := prefix.Addr()
	ip := net.IP(addr.AsSlice())
	bits := 32
	if addr.Is6() {
		bits = 128
	}
	return &net.IPNet{IP: ip, Mask: net.CIDRMask(prefix.Bits(), bits)}
}

// underlayGeneration 被手抄成了两份:supervisor 那份短路 darwinUnderlayPlan(要不要
// **执行**重绑),guardian 这份短路 NetworkObserver.checkGeneration(要不要**发起**
// 恢复)。只修一份的后果是行为一个字都不变 —— 恢复根本不会被请求,执行那份再正确
// 也永远轮不到它跑。2026-08-19 真机:同网段换 IP,隧道死了 5.5 小时无人问津。
func TestDarwinObserverPrefixTextsKeepIPv4HostBits(t *testing.T) {
	before, err := darwinObserverPrefixTexts([]net.Addr{ipNet(t, "192.168.50.37/24")})
	if err != nil {
		t.Fatal(err)
	}
	after, err := darwinObserverPrefixTexts([]net.Addr{ipNet(t, "192.168.50.38/24")})
	if err != nil {
		t.Fatal(err)
	}
	if before[0] == after[0] {
		t.Fatalf("host address change left the observer fingerprint at %q; recovery is never requested", before[0])
	}
}

// 与 supervisor 侧那条同向:v6 临时地址按天轮换,让它进指纹就是每天定时重建隧道。
func TestDarwinObserverPrefixTextsCollapseIPv6PrivacyAddresses(t *testing.T) {
	before, err := darwinObserverPrefixTexts([]net.Addr{
		ipNet(t, "192.168.50.38/24"), ipNet(t, "2001:db8:1:2::1111/64"),
	})
	if err != nil {
		t.Fatal(err)
	}
	after, err := darwinObserverPrefixTexts([]net.Addr{
		ipNet(t, "192.168.50.38/24"), ipNet(t, "2001:db8:1:2::eeee/64"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != len(after) {
		t.Fatalf("prefix counts diverged: %#v vs %#v", before, after)
	}
	for i := range before {
		if before[i] != after[i] {
			t.Fatalf("IPv6 privacy rotation moved the observer fingerprint: %#v vs %#v", before, after)
		}
	}
}

// **防漂移守卫。** 这两份判据永远不会被互相比对(观测者的指纹进 recovery 请求,
// supervisor 的指纹进 plan 短路),所以任何一方单独改动都不会有测试报错 —— 而
// 它们不一致的后果是「请求了恢复,执行时短路成空操作」或者反过来,两种都是静默的。
// 唯一挡得住的办法是钉住它们规范化同一个地址得到同一个串。
func TestDarwinObserverCanonicalisationMatchesSupervisor(t *testing.T) {
	for _, cidr := range []string{
		"192.168.50.37/24",
		"192.168.50.38/24",
		"10.84.6.187/16",
		"172.20.10.2/28",
		"2001:db8:1:2::1111/64",
		"fe80::1/64",
	} {
		got, err := darwinObserverPrefixTexts([]net.Addr{ipNet(t, cidr)})
		if err != nil {
			t.Fatalf("%s: %v", cidr, err)
		}
		want, err := supervisor.CanonicalUnderlayPrefix(netip.MustParsePrefix(cidr))
		if err != nil {
			t.Fatalf("%s: %v", cidr, err)
		}
		if got[0] != want.String() {
			t.Fatalf("%s: observer says %q, supervisor says %q", cidr, got[0], want.String())
		}
	}
}
