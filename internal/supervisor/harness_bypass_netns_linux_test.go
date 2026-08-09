//go:build integration && linux

// harness_bypass_netns_linux_test.go 钉住**换服务器**这条路径上的两条防环性质:
// 已知服务器之间切换一条路由都不该动,而切到清单外的新服务器时,bypass 路由必须
// 在传输被换过去**之前**就已经装好。
//
// 两条都是关于内核状态的,单元测试证明不了。它们防的是同一类 bug:隧道自己连
// 服务器的流量被劫进 TUN = 成环,而成环是**静默**的 —— 连得上、status 显绿、
// 没有任何一处报错。
//
// 「每台配置过的服务器都在 bypass 里」那条在 harness_netns_linux_test.go 的
// TestHarnessBypassCoversEveryConfiguredServerElseSilentLoop,不在这里重复一遍。
package supervisor

import (
	"slices"
	"strings"
	"testing"
)

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

// 断言 3:切到**清单外**的新服务器时,它的 bypass 路由必须在传输被换过去之前就装好。
//
// 为什么必须是清单外的那台:清单里的两台在启动时就已经全部铺好,切过去时 bypass
// 集合根本没变(changed=false),`handleSetServer` 里那段「先 Rehijack 再换传输」的
// 代码整个不执行 —— 上面那条断言 2 因此**碰不到**顺序这件事(实测:把
// composeMutations 的两对参数对调,断言 2 照样全绿)。清单外的目标让集合真的变化,
// 顺序保证那条路径才第一次被走到。
//
// 为什么断言打在「建隧道那一刻」:两种顺序的**终态一模一样**,路由最后都装上了。
// 差别只是一个窗口 —— 传输已经换到新服务器、而它的 bypass 路由还没装,那一瞬隧道
// 自己连服务器的流量被劫进 TUN。任何只比 before/after 的断言在结构上都看不见窗口。
// swapTo 正是在窗口里调假隧道工厂的,故 RoutesAtBuild 就是站在窗口中间问内核。
func TestHarnessSwitchToNewServerInstallsBypassBeforeSwappingTransport(t *testing.T) {
	enterIsolatedNetns(t)
	h := startHarness(t, twoServerHarnessConfig)
	defer h.stop()

	// 清单外(config 里只有 .10/.11/.12/.13),且是 IP 字面量 —— netns 里没有 DNS,
	// 解析必须不经网络。unionRequired 会把端点点名的链接并进来并标 Required。
	// **切两次,而且两次都是清单外的。**
	//
	// 只切一次的话,任何「第一次 Rehijack 之后才引入的陈旧」在结构上都看不见 ——
	// 复审用一个延迟冻结的变异(liveMutator.currentServerBypass 首次读取时把 store
	// 换成快照)证明过:单次切换版本全绿,而第二次切换时新服务器的路由根本没装上,
	// 隧道自己连服务器的流量被劫进 TUN,`bx status` 却仍然绿。
	// 这条正是被删掉的那条 AST 守卫(禁止包内给 .store 赋值)原本覆盖的性质 ——
	// 它管的是「任何时刻都不许」,而只切一次只能证明「启动那一刻没有」。
	for _, target := range []struct{ link, ip string }{
		{"vless://u@203.0.113.20:443", "203.0.113.20"},
		{"vless://u@203.0.113.30:443", "203.0.113.30"},
	} {
		assertBypassInstalledBeforeSwap(t, h, target.link, target.ip)
	}
}

func assertBypassInstalledBeforeSwap(t *testing.T, h *harness, target, targetIP string) {
	t.Helper()
	// 切换前它不该在表里,否则「建隧道时已经在」是从一开始就成立的,断言等于没测。
	if before := ipOut(t, "route", "show", "table", itoa(routeTable)); strings.Contains(before, targetIP) {
		t.Fatalf("%s 切换前就已经在 bypass 里了,这条断言会失去意义:\n%s", targetIP, before)
	}
	if _, err := SetServerControl(h.sockPath, target, ""); err != nil {
		t.Fatalf("切到清单外的新服务器应成功: %v", err)
	}
	if _, err := CommitControl(h.sockPath); err != nil {
		t.Fatalf("确认应成功: %v", err)
	}

	var built *fakeTunnelRequest
	for _, r := range h.tunnels.snapshot() {
		if r.Link == target {
			built = &r
			break
		}
	}
	// 「工厂从没被要求建目标」与「建了但路由没装上」是两个不同的 bug,消息必须分得清:
	// 前者说明切换整个被拒/被跳过,顺序无从谈起,此时若沉默地当成通过就是假绿。
	if built == nil {
		t.Fatalf("假隧道工厂从没被要求建 %s —— 切换根本没发生(被拒?被跳过?),顺序无从谈起;建过的=%v",
			target, h.tunnels.links())
	}
	// 观测本身失败必须响亮:否则「问不出来」会被读成「没找到」,而本断言恰好
	// 以「没找到即失败」为极性 —— 今天它因此侥幸安全,但下一个反极性的断言不会。
	if built.RoutesAtBuildErr != nil {
		t.Fatalf("建隧道那一刻读不到路由表,无从判断顺序: %v", built.RoutesAtBuildErr)
	}
	if !strings.Contains(built.RoutesAtBuild, targetIP) {
		t.Fatalf("换传输到 %s 那一刻,它的 bypass 路由还没装上 —— 这个窗口里隧道自己连服务器的流量会被劫进 TUN(静默成环)\n建隧道那一刻 table %d=\n%s",
			targetIP, routeTable, built.RoutesAtBuild)
	}
}
