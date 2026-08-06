package guardian

import "testing"

// 观测层要问"内核里有没有这些 reject 路由",必须与屏障实际装的是同一份清单。
// 若二者漂移,观测会问错网段,得出错误结论。
func TestBlockingBarrierCIDRsMatchesInstalledBlocks(t *testing.T) {
	ipv4, ipv6 := BlockingBarrierCIDRs()
	if len(ipv4) != len(publicIPv4Blocks) {
		t.Fatalf("IPv4 网段数 = %d, want %d", len(ipv4), len(publicIPv4Blocks))
	}
	for i, want := range publicIPv4Blocks {
		if ipv4[i] != want {
			t.Errorf("IPv4[%d] = %q, want %q", i, ipv4[i], want)
		}
	}
	if len(ipv6) != len(publicIPv6Blocks) {
		t.Fatalf("IPv6 网段数 = %d, want %d", len(ipv6), len(publicIPv6Blocks))
	}
	for i, want := range publicIPv6Blocks {
		if ipv6[i] != want {
			t.Errorf("IPv6[%d] = %q, want %q", i, ipv6[i], want)
		}
	}
}

// 返回的必须是副本:调用方改动不得影响屏障实际使用的清单。
func TestBlockingBarrierCIDRsReturnsCopy(t *testing.T) {
	ipv4, _ := BlockingBarrierCIDRs()
	if len(ipv4) == 0 {
		t.Fatal("IPv4 网段不应为空")
	}
	original := publicIPv4Blocks[0]
	ipv4[0] = "203.0.113.0/24"
	if publicIPv4Blocks[0] != original {
		t.Error("修改返回值污染了屏障实际使用的清单")
	}
}
