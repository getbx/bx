//go:build linux

package leakserve

import "testing"

// 判据按**真实输出**定,不按记忆(2026-08-12 特权容器实测)。
func TestParseLinuxRoutes(t *testing.T) {
	entries := parseLinuxRoutes(`0.0.0.0/1 dev bx0 table 100 scope link 
default via 172.17.0.1 dev eth0 
172.17.0.0/16 dev eth0 scope link  src 172.17.0.4 
local 127.0.0.0/8 dev lo table local scope host  src 127.0.0.1 
broadcast 127.255.255.255 dev lo table local scope link  src 127.0.0.1 
unreachable default dev lo table 100 
64.0.0.0/2 via 172.17.0.1 dev eth0 
`, "0.0.0.0/0")

	byDest := map[string]int{}
	for i, e := range entries {
		byDest[e.Destination] = i
	}

	// **local / broadcast 是内核 local 表的东西,不是转发路由。** 带进来只会把
	// 证据区冲掉,而它们从不参与竞争。
	for _, gone := range []string{"127.0.0.0/8", "127.255.255.255"} {
		if _, ok := byDest[gone]; ok {
			t.Errorf("local/broadcast 路由被带进来了:%s", gone)
		}
	}
	// default 按传进来的族归一化。
	//
	// **不能按目的地去 map 里找**:`default` 与 `unreachable default` 归一化之后
	// 同名,后写覆盖前写 —— 而它们是两条完全不同的路由(一条送、一条扔)。
	// 这正是把「阻断」从目的地里分出来当独立字段的理由。
	var forwardedDefault *int
	for i := range entries {
		if entries[i].Destination == "0.0.0.0/0" && !entries[i].Blocking {
			idx := i
			forwardedDefault = &idx
		}
	}
	if forwardedDefault == nil {
		t.Fatalf("default 没有被归一化:%+v", entries)
	}
	if entries[*forwardedDefault].Interface != "eth0" {
		t.Errorf("default 的接口 = %q, want eth0", entries[*forwardedDefault].Interface)
	}
	// **unreachable 是阻断,不是 claim** —— bx 自己的 v6 屏障就是这个形状。
	// 注意它的目的地也是 default,与上面那条同名,所以要按 Blocking 找。
	var sawBlocking bool
	for _, e := range entries {
		if e.Blocking {
			sawBlocking = true
			if e.Interface != "lo" {
				t.Errorf("unreachable 那条的接口 = %q, want lo", e.Interface)
			}
		}
	}
	if !sawBlocking {
		t.Error("unreachable default 没有被标成 Blocking —— 会被当成有人在抢")
	}
	// Linux 没有 RTF_IFSCOPE,**不许合成一个假的**。
	for _, e := range entries {
		if e.Scoped {
			t.Errorf("Linux 上不该有 Scoped:%+v", e)
		}
	}
	// bx 自己的劫持路由(table 100)照常带出来 —— 它属于 bx0,判据据此排除自己。
	if i, ok := byDest["0.0.0.0/1"]; !ok || entries[i].Interface != "bx0" {
		t.Errorf("bx 自己的劫持路由没读出来:%+v", entries)
	}
}

// **没有 dev 的行跳过,不编一个接口名。**
func TestParseLinuxRoutesSkipsRoutesWithoutADevice(t *testing.T) {
	got := parseLinuxRoutes("blackhole 10.0.0.0/8 \nsomething weird here\n", "0.0.0.0/0")
	for _, e := range got {
		if e.Interface == "" {
			t.Errorf("产出了没有接口的条目:%+v", e)
		}
	}
}

// `ip route get` 是「谁在管这条路」唯一权威的答案 —— 它经过策略路由,
// 而 bx 在 Linux 上正是靠 ip rule 分流的,读 main 表会得出完全错误的结论。
func TestParseLinuxRouteGetInterface(t *testing.T) {
	if got := parseLinuxRouteGetInterface("1.1.1.1 dev bx0  src 198.51.100.1 \n    cache \n"); got != "bx0" {
		t.Fatalf("= %q, want bx0", got)
	}
	if got := parseLinuxRouteGetInterface("1.1.1.1 via 172.17.0.1 dev eth0 src 172.17.0.4 uid 0 \n"); got != "eth0" {
		t.Fatalf("= %q, want eth0", got)
	}
	// 问不出来就是空 —— 调用方据此报「没问出来」,而不是编一个接口。
	if got := parseLinuxRouteGetInterface("RTNETLINK answers: Network is unreachable\n"); got != "" {
		t.Fatalf("= %q, want 空", got)
	}
}
