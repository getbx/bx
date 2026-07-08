package supervisor

import (
	"net/netip"
	"testing"
)

// 不变量守卫:TUN DNS 哨兵必须是会路由进 TUN 的公网 IPv4——不在私网 bypass 段(privateNoBind),
// 否则 DNS 查询会绕过 TUN 走物理网卡(且被 WFP 封)→ DNS 断。改哨兵时此测试兜底。
func TestTunDNSSentinelRoutesIntoTun(t *testing.T) {
	ip, err := netip.ParseAddr(tunDNSSentinel)
	if err != nil {
		t.Fatalf("哨兵不是合法 IP: %v", err)
	}
	if !ip.Is4() {
		t.Fatalf("哨兵应为 IPv4(SetDNS AF_INET),got %s", tunDNSSentinel)
	}
	if privateNoBind.Contains(ip) {
		t.Fatalf("%s 在私网 bypass 段,会绕过 TUN,DNS 进不了 bx", tunDNSSentinel)
	}
}
