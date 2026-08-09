//go:build integration && linux

// harness_bypass_netns_linux_test.go 是集成台上的第一批**实质断言**:
// 「每台配置过的服务器都在 bypass 里」与「已知服务器之间切换一条路由都不动」。
//
// 两条都是关于**内核状态**的,单元测试证明不了 —— 此前保护它们的全部是对抽出来的
// 纯函数的断言,而那一类 bug(漏一条 bypass = 隧道自己的流量被劫进 TUN = 成环)
// 是静默的:连得上、status 显绿、流量绕圈,没有任何地方报错。
package supervisor

import (
	"slices"
	"strings"
	"testing"
)

// 断言 1:清单里**每一台**服务器的**两条**链接对应的 IP,都真的在内核路由表里。
// 少一条 = 切过去之后隧道自己的流量被劫进 TUN = 成环,而成环是静默的
// (连得上、status 显绿、流量绕圈)。这条走过五轮修复,此前只有单元测试背书。
func TestHarnessBypassCoversEveryConfiguredServer(t *testing.T) {
	enterIsolatedNetns(t)
	h := startHarness(t, twoServerHarnessConfig)
	defer h.stop()

	routes := ipOut(t, "route", "show", "table", itoa(routeTable))
	for _, ip := range []string{"203.0.113.10", "203.0.113.11", "203.0.113.12", "203.0.113.13"} {
		if !strings.Contains(routes, ip) {
			t.Errorf("服务器 %s 不在 bypass 里 —— 切过去就成环(静默)\ntable %d=\n%s", ip, routeTable, routes)
		}
	}
}

// 断言 2:已知服务器之间切换**一条路由都不该动**(启动时已全部铺好)。
// 这是「全铺好」那条设计的唯一实证 —— 单元测试证明不了它,因为它是关于内核状态的。
func TestHarnessSwitchBetweenKnownServersDoesNotTouchRoutes(t *testing.T) {
	enterIsolatedNetns(t)
	h := startHarness(t, twoServerHarnessConfig)
	defer h.stop()

	snapshot := func() string {
		return ipOut(t, "rule", "list") +
			ipOut(t, "route", "show", "table", itoa(routeTable)) +
			ipOut(t, "-6", "route", "show", "table", itoa(routeTable))
	}
	before := snapshot()
	if _, err := SetServerControl(h.sockPath, "vless://u@203.0.113.12:443", "hysteria2://u@203.0.113.13:443"); err != nil {
		t.Fatalf("切到已知服务器应成功: %v", err)
	}
	if _, err := CommitControl(h.sockPath); err != nil {
		t.Fatalf("确认应成功: %v", err)
	}
	if after := snapshot(); before != after {
		t.Fatalf("已知服务器之间切换不该动任何路由\n--- before ---\n%s\n--- after ---\n%s", before, after)
	}
	// 切过去这件事本身也要有证据,否则「什么都没发生」同样能让上面那条通过。
	if links := h.tunnels.links(); !slices.Contains(links, "vless://u@203.0.113.12:443") {
		t.Fatalf("没有证据表明真的切到了 beta:建过的链接=%v", links)
	}
}
