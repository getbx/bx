//go:build linux

package supervisor

import (
	"errors"
	"testing"
)

// 三种输出按**真实行为**区分(2026-08-12 容器实测的原样输出)。
//
// 此前这三条路径里有两条零覆盖:标准套件只查一个正常可达的地址,既不会命中
// unreachable、也不会走到「没拿到接口」—— 把它们改坏,整套测试全绿(变异实测)。
func TestParseIPRouteGet(t *testing.T) {
	t.Run("正常", func(t *testing.T) {
		sel, err := parseIPRouteGet("1.1.1.1 via 172.17.0.1 dev eth0  src 172.17.0.4 \n", nil)
		if err != nil {
			t.Fatal(err)
		}
		if sel.Interface != "eth0" || sel.Gateway != "172.17.0.1" || sel.Reject {
			t.Fatalf("= %+v", sel)
		}
	})

	t.Run("经策略路由进 bx 的 TUN(无网关)", func(t *testing.T) {
		sel, err := parseIPRouteGet("1.1.1.1 dev bx0  src 198.51.100.1 \n    cache \n", nil)
		if err != nil {
			t.Fatal(err)
		}
		if sel.Interface != "bx0" || sel.Gateway != "" {
			t.Fatalf("= %+v", sel)
		}
	})

	// **命中 unreachable 路由 → Reject,不是「没有路由」。**
	// 那是 bx 自己 v6 屏障的形状;混为一谈就分不清「这台机器没有 v6」与
	// 「bx 正在挡住 v6」,而 observe 的 barrier_present 要靠这个区别。
	t.Run("命中 unreachable 路由", func(t *testing.T) {
		sel, err := parseIPRouteGet("ip: RTNETLINK answers: No route to host\n", errors.New("exit status 2"))
		if err != nil {
			t.Fatalf("应返回 Reject 而不是错误:%v", err)
		}
		if !sel.Reject {
			t.Fatalf("没标 Reject:%+v", sel)
		}
	})

	t.Run("压根没有路由", func(t *testing.T) {
		_, err := parseIPRouteGet("ip: RTNETLINK answers: Network is unreachable\n", errors.New("exit status 2"))
		if !errors.Is(err, ErrNoRouteToDestination) {
			t.Fatalf("= %v, want ErrNoRouteToDestination", err)
		}
	})

	// **零值不许冒充「查到了」。**
	t.Run("读不懂的输出", func(t *testing.T) {
		sel, err := parseIPRouteGet("something entirely unexpected\n", nil)
		if err == nil {
			t.Fatalf("读不懂却报成功:%+v", sel)
		}
	})

	// 命令本身失败(ip 不存在之类)如实上报,不冒充任何结论。
	t.Run("命令失败", func(t *testing.T) {
		want := errors.New("exec: \"ip\": executable file not found in $PATH")
		if _, err := parseIPRouteGet("", want); !errors.Is(err, want) {
			t.Fatalf("= %v, want 原样上报", err)
		}
	})
}
