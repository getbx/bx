package supervisor

import (
	"strings"
	"testing"
)

// 接管播报必须与**这一次实际生效的分流方式**一致。
//
// 起因是项目所有者 2026-08-31 升级后读日志当场发现的:那行播报无条件打印
// 「中国 IP 直连,其余走 bx 隧道」,而他跑的是 global —— china 列表整个不加载。
// 铁证就在同一份日志里隔两行:
//
//	分流脑就绪: 模式=全局(除内网/用户 direct 外一切走代理) china_domain=0 china_cidr=0
//	✅ bx 已全局接管。中国 IP 直连,其余走 bx 隧道。
//
// **一句关于系统当前行为的假话,比不说更糟**:用户会据此以为国内流量在直连,
// 于是去别处找「为什么访问国内站点慢」的原因,而真实原因正是它们全走了隧道。
// 这与本仓库反复记的「关于代码的陈述」是同一类失效,只是这次的读者是用户。

func TestTakeoverSummaryNeverClaimsChinaDirectInGlobalMode(t *testing.T) {
	got := takeoverSummary(true, "host")
	if strings.Contains(got, "中国 IP 直连") {
		t.Fatalf("global 模式下 china 列表根本不加载,不许说中国 IP 直连: %q", got)
	}
	// 说清楚**真正**直连的是哪两类 —— 与 proxyMode 那行「除内网/用户 direct
	// 外一切走代理」对上,两处措辞不许各说各的。
	if !strings.Contains(got, "内网") || !strings.Contains(got, "direct") {
		t.Fatalf("global 播报没说清真正直连的是什么: %q", got)
	}
}

func TestTakeoverSummaryStillDescribesSplitModeCorrectly(t *testing.T) {
	got := takeoverSummary(false, "host")
	if !strings.Contains(got, "中国 IP 直连") {
		t.Fatalf("split 模式下这句话是真的,不该被这次修复连累掉: %q", got)
	}
}

// router 模式只劫持 LAN 转发流量,路由器自身流量不碰 —— 说「全局接管」
// 会让人以为这台路由器自己的出站也走了隧道。
func TestTakeoverSummaryDistinguishesRouterMode(t *testing.T) {
	for _, global := range []bool{false, true} {
		got := takeoverSummary(global, "router")
		if strings.Contains(got, "全局接管") {
			t.Fatalf("router 模式(global=%v)不该说全局接管: %q", global, got)
		}
		if !strings.Contains(got, "LAN") {
			t.Fatalf("router 模式(global=%v)没说清接管的是 LAN 转发: %q", global, got)
		}
	}
	// router + global:分流方式仍是 global,那一半也要说对。
	if got := takeoverSummary(true, "router"); strings.Contains(got, "中国 IP 直连") {
		t.Fatalf("router-global 仍然不该说中国 IP 直连: %q", got)
	}
}

// 四种组合各有各的话,不许两种塌成同一句 —— 塌了就说明有一种模式在被
// 另一种模式的描述冒名顶替。
func TestTakeoverSummaryIsDistinctPerMode(t *testing.T) {
	seen := map[string]string{}
	for _, c := range []struct {
		global bool
		mode   string
	}{{false, "host"}, {true, "host"}, {false, "router"}, {true, "router"}} {
		got := takeoverSummary(c.global, c.mode)
		key := proxyMode(c.global, c.mode)
		if prev, dup := seen[got]; dup {
			t.Fatalf("%s 与 %s 的播报是同一句:%q", key, prev, got)
		}
		seen[got] = key
	}
}
