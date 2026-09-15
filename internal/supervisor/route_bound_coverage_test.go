package supervisor

import (
	"os"
	"strings"
	"testing"
)

// **Windows 必须有自己的 PhysicalDefaultRoute,不许落回那份「不支持」兜底。**
//
// 它此前就落在 `!darwin && !linux` 那份上、恒报不支持,后果是 `bx explain`
// 在 Windows 上永远说不出「这块是物理网卡」——pathview 的接口归属靠
// `Facts.PhysicalDev` 对上名字,而那一格一直是空的。2026-09-15 真机实测的
// 原话是「普通程序连它会走 WLAN,**我认不出这个接口是什么**」,而那台机器上
// WLAN 就是物理 Wi-Fi、bx 自己的 Hijack 每次都正确地挑中它。
//
// **这条回归是静默的**:把 build tag 改回去,darwin/linux 全绿、Windows 上
// 那句话悄悄退回「认不出」,而没有任何测试会红 —— 本平台的行为测试在本机
// 根本不跑。故判据打在**构造**上。
func TestWindowsHasItsOwnPhysicalDefaultRoute(t *testing.T) {
	fallback, err := os.ReadFile("route_bound_other.go")
	if err != nil {
		t.Fatalf("读不出 route_bound_other.go:%v —— 这条守卫读不懂现在的代码了,先修它", err)
	}
	line := strings.SplitN(string(fallback), "\n", 2)[0]
	if !strings.HasPrefix(line, "//go:build") {
		t.Fatalf("第一行不是 build tag,守卫的锚点漂了:%q", line)
	}
	if !strings.Contains(line, "!windows") {
		t.Errorf("那份「不支持」兜底又把 Windows 收回去了(%q)—— explain 会悄悄退回「认不出这个接口」", line)
	}

	win, err := os.ReadFile("route_bound_windows.go")
	if err != nil {
		t.Fatalf("Windows 那份实现不见了:%v", err)
	}
	src := string(win)
	if !strings.Contains(src, "physicalDefaultRoute()") {
		t.Error("Windows 那份没有复用 Hijack 用的 physicalDefaultRoute() —— " +
			"第二份实现会与它漂开,而漂开的后果是 explain 说的那块网卡与 bx 真正用的不是同一块")
	}
	// **不许在这儿再走一遍路由表。** 那份共用实现里写着 Windows 的有效 metric
	// 是「路由 metric + 接口 metric」,只比前者会在多网卡上错选 —— 重写一遍
	// 就是把那条教训再付一次。
	if strings.Contains(src, "GetIPForwardTable2") {
		t.Error("Windows 那份自己又走了一遍路由表 —— metric 那条教训会被重新踩一次")
	}
}
