package supervisor

import (
	"net/netip"
	"testing"
)

// 2026-08-19 真机事故:项目所有者的 Mac 开着「私有 Wi-Fi 地址」,MAC 每隔一两小时
// 轮换一次,路由器就发一个新 IP —— 192.168.50.14 → .16 → .18 → .37 → .38,同一个
// Wi-Fi、同一个网关、同一个网段。每换一次,sing-box 的 socket 就绑在一个已经不
// 存在的源地址上,日志里连着几小时的
// `write udp 192.168.50.16:…->203.0.113.92:443: write: can't assign requested address`
// (8-17 一天 45011 条,连续 5.5 小时),而 `network_recovery` 一次都没触发,
// 用户只能 `sudo bx down && sudo bx up`(8-10 至今 28 次)。
//
// 闸门只有一处:underlayGeneration 既是 NetworkObserver.checkGeneration 的触发判据,
// 也是 darwinUnderlayPlan 头一句的短路判据。它当时把主机位 Masked() 掉了,于是
// .37/24 与 .38/24 是同一个指纹 —— **换了源地址,而"物理路径"这个身份说没变**。
func TestUnderlayGenerationMovesWhenOnlyTheHostAddressChanges(t *testing.T) {
	before := mustUnderlaySnapshot(t, "en0", "192.168.50.1", "192.168.50.37/24")
	after := mustUnderlaySnapshot(t, "en0", "192.168.50.1", "192.168.50.38/24")

	if before.Generation == after.Generation {
		t.Fatalf("host address change left the generation at %q; recovery can never fire", before.Generation)
	}
}

// 触发器与执行计划共用同一个判据,所以两边都要钉:只证明 generation 动了,
// 而 plan 仍然短路,用户看到的行为一个字都不会变。
func TestDarwinUnderlayPlanRebindsWhenOnlyTheHostAddressChanges(t *testing.T) {
	before := mustUnderlaySnapshot(t, "en0", "192.168.50.1", "192.168.50.37/24")
	after := mustUnderlaySnapshot(t, "en0", "192.168.50.1", "192.168.50.38/24")

	plan, err := darwinUnderlayPlan(before, after, []string{"203.0.113.92/32"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) == 0 {
		t.Fatal("same-subnet host address change planned nothing; server bypass stays pinned to the dead source address")
	}
	var sawServerBypass bool
	for _, command := range allDarwinUnderlayCommandTexts(plan) {
		if command == "route -n change -net 203.0.113.92/32 192.168.50.1" {
			sawServerBypass = true
		}
	}
	if !sawServerBypass {
		t.Fatalf("plan did not rebind the server bypass: %#v", darwinUnderlayCommandTexts(plan))
	}
}

// 反向守卫。**方向是刻意的**:v4 主机位进指纹是为了让恢复能触发,而 v6 临时地址
// (RFC 4941 privacy address)在 macOS 上本来就按天轮换 —— 让它也进指纹,就会每天
// 平白重建一次隧道,而重建隧道正是掐断 SSE/WebSocket 的那个动作,等于用这次修复
// 制造出它要消灭的症状。bx 的 v6 是 fail-closed 阻断的,v6 主机位对出口选路没有
// 任何影响,只有网段身份有意义。
func TestUnderlayGenerationIgnoresIPv6TemporaryAddressRotation(t *testing.T) {
	before := mustUnderlaySnapshot(t, "en0", "192.168.50.1",
		"192.168.50.38/24", "2001:db8:1:2::1111/64")
	after := mustUnderlaySnapshot(t, "en0", "192.168.50.1",
		"192.168.50.38/24", "2001:db8:1:2::eeee/64")

	if before.Generation != after.Generation {
		t.Fatal("IPv6 privacy address rotation moved the generation; the tunnel would be rebuilt on a schedule and every SSE/WebSocket stream cut with it")
	}
}

// darwinUnderlayPlan 拿到的 LocalCIDRs 已经规范化过一遍,它自己又规范化一遍。
// 不幂等的话,同一份快照会算出两个指纹,短路判据就永远成立不了 —— 这是把
// 「保留主机位」这条改动拆坏的最省事方式,值得单独钉一条。
func TestCanonicalUnderlayPrefixIsIdempotent(t *testing.T) {
	for _, raw := range []string{
		"192.168.50.38/24",
		"10.84.6.187/16",
		"::ffff:192.168.50.38/120",
		"2001:db8:1:2::1111/64",
		"fe80::1/64",
	} {
		once, err := canonicalUnderlayPrefix(netip.MustParsePrefix(raw))
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		twice, err := canonicalUnderlayPrefix(once)
		if err != nil {
			t.Fatalf("%s (second pass): %v", raw, err)
		}
		if once != twice {
			t.Fatalf("%s canonicalised to %s then %s", raw, once, twice)
		}
	}
}
