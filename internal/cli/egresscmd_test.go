package cli

import (
	"strings"
	"testing"

	"github.com/getbx/bx/internal/setup"
)

// **`bx egress add` 的地址必须是位置参数,不能是 Required flag。**
//
// urfave/cli 遇到第一个位置参数就停止解析 flag,于是
// `bx egress add office --socks5 …` 里那个 flag 根本不会被看见 —— 命令直接
// 失败,报的还是「没给 --socks5」,而用户明明写了。
//
// 这个坑本仓库 2026-08-14 在 `bx setup … --udp` 上栽过一次(bx 打印出一条
// 它自己会拒绝的命令);实跑 egress 时又栽了一次。守卫钉住它不许长回来。
func TestEgressAddTakesPositionalArgsNotRequiredFlags(t *testing.T) {
	for _, cmd := range egressCommands() {
		if cmd.Name != "add" {
			continue
		}
		for _, f := range cmd.Flags {
			if sf, ok := f.(interface{ IsRequired() bool }); ok && sf.IsRequired() {
				t.Fatalf("`egress add` 带了 Required flag —— 它在位置参数之后不会被解析,"+
					"用户写了也等于没写:%v", f.Names())
			}
		}
		if !strings.Contains(cmd.ArgsUsage, "<socks5>") {
			t.Errorf("用法里没写出地址是位置参数:%q", cmd.ArgsUsage)
		}
		return
	}
	t.Fatal("没有 add 子命令 —— 守卫已经失效,先修守卫")
}

// **空清单要说清它不是首选。**
//
// 一个空列表加一句用法,会让人以为这就是访问内网的正路;而绝大多数情况下
// Tailscale 才是,且 bx 不挡它。
func TestEmptyEgressListPointsAtMeshFirst(t *testing.T) {
	out := renderEgressList(nil)
	if !strings.Contains(out, "mesh") {
		t.Errorf("没提 mesh 才是首选:\n%s", out)
	}
	if !strings.Contains(out, "bx egress add") {
		t.Errorf("没给出确实要用时怎么写:\n%s", out)
	}
}

// **配了出口却没给它网段 = 它什么都不做,必须说出来。**
// 不说的话用户会以为配好了,然后去查一个根本没生效的功能。
func TestEgressWithoutRoutesSaysItDoesNothing(t *testing.T) {
	out := renderEgressList([]setup.EgressEntry{{Name: "office", Socks5: "127.0.0.1:1080"}})
	if !strings.Contains(out, "什么都不做") {
		t.Errorf("没说清它现在不生效:\n%s", out)
	}
}

// 改完要说重连 —— bx 不热重载,而「已保存却什么都没变」会让人以为功能坏了。
func TestEgressChangesSayReconnect(t *testing.T) {
	if !strings.Contains(egressRestartHint, "bx down") || !strings.Contains(egressRestartHint, "bx up") {
		t.Fatalf("没告诉用户怎么让它生效:%q", egressRestartHint)
	}
}
