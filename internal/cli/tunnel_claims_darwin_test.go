//go:build darwin

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// **路由表是权威,进程清单只回答另一个问题。**
//
// 此前这里全是猜测式措辞:「may create another tunnel when connected」
// 「verify its routes do not bypass bx」—— 都是让**用户**去查一件 bx 自己查得到的事。
// 而按行为分类之后,「它有没有在抢公网流量」有确定答案。
//
// 进程在跑但还没连上时路由表里什么都没有 —— 那是进程清单唯一还值钱的地方,
// 而措辞必须如实说成「还没 claim 任何东西」,不是「可能会抢」。
func TestCompetingTunnelWordingPointsAtTheObservableAnswer(t *testing.T) {
	claiming := darwinTunnelClaimChecks(`Internet:
Destination        Gateway            Flags        Netif
0/1                utun11             UScg        utun11
0/1                utun4              USc          utun4
128.0/1            utun4              USc          utun4
100.64/10          utun6              USc          utun6
`, "utun11")

	if len(claiming) != 1 {
		t.Fatalf("应只报出真的在抢公网流量的那一条,得到 %+v", claiming)
	}
	got := claiming[0]
	if !strings.Contains(got.Detail, "utun4") {
		t.Errorf("没点名是哪个接口:%q", got.Detail)
	}
	if strings.Contains(got.Detail, "utun6") {
		t.Errorf("只 claim CGNAT 的隧道被报成在抢:%q —— 那是叠加不是竞争", got.Detail)
	}
	if strings.Contains(got.Detail, "utun11") {
		t.Errorf("把 bx 自己报进去了:%q", got.Detail)
	}
	// **措辞必须是行为,不是产品。** 同一种行为可能来自任何产品。
	if !strings.Contains(strings.ToLower(got.Detail), "public") {
		t.Errorf("没说出可观测的行为:%q", got.Detail)
	}
}

// 没有别的隧道在抢时**一个字都不说** —— 共存正常是默认预期。
func TestNoClaimCheckWhenNothingIsCompeting(t *testing.T) {
	got := darwinTunnelClaimChecks(`Internet:
Destination        Gateway            Flags        Netif
0/1                utun11             UScg        utun11
100.64/10          utun6              USc          utun6
10                 192.168.1.1        UGSc          en0
`, "utun11")
	if len(got) != 0 {
		t.Fatalf("没有人在抢却出了检查项:%+v", got)
	}
}

// 读不到路由表时不许假装「没有人在抢」—— 那与「问过了,确实没有」必须分开。
func TestUnreadableRouteTableIsNotSilence(t *testing.T) {
	got := darwinTunnelClaimChecks("", "utun11")
	if len(got) != 1 || got[0].Status != "info" {
		t.Fatalf("路由表读不到时应如实说明,得到 %+v", got)
	}
	if !strings.Contains(strings.ToLower(got[0].Detail), "not") {
		t.Errorf("没说清是「没问出来」:%q", got[0].Detail)
	}
}

// **bx 关着时,路由表里的隧道全是别人的。**
//
// 那时控制 socket 拨不通、bxTun 是空串,而这是正常场景(`bx leak-check` 在 bx 关着时
// 运行是这个功能的主用途之一)。此时不排除任何接口是对的 —— 根本没有 bx 的隧道。
func TestWithBXOffEveryClaimingTunnelIsSomebodyElses(t *testing.T) {
	got := darwinTunnelClaimChecks(`Internet:
Destination        Gateway            Flags        Netif
0/1                utun4              USc          utun4
128.0/1            utun4              USc          utun4
`, "")
	if len(got) != 1 || got[0].Status != "warn" {
		t.Fatalf("bx 关着时别人的全隧道应照常报出来,得到 %+v", got)
	}
	if !strings.Contains(got[0].Detail, "utun4") {
		t.Errorf("没点名:%q", got[0].Detail)
	}
}

// **产品清单不许再猜。** 它回答「谁在跑」,而「谁在抢」由路由表回答 ——
// 此前那些「may create another tunnel」「verify its routes do not bypass bx」
// 是让用户去查一件 bx 自己查得到的事。
func TestProductListStoppedSpeculating(t *testing.T) {
	raw, err := os.ReadFile("platform_check_darwin.go")
	if err != nil {
		t.Fatalf("读不到源码:%v —— 守卫失去意义,必须响亮失败", err)
	}
	source := string(raw)
	for _, gone := range []string{
		"may create another tunnel when connected",
		"verify its routes do not bypass bx",
		"it may create a path outside bx",
	} {
		if strings.Contains(source, gone) {
			t.Errorf("猜测式措辞又回来了:%q —— 那件事路由表答得了,别让用户去查", gone)
		}
	}
	// 反面:必须指向那个权威答案,否则用户不知道去哪看。
	if !strings.Contains(source, "tunnel_claims") {
		t.Error("产品清单没有指向 tunnel_claims —— 用户只知道「它在跑」,不知道下一步看哪")
	}
}

// **删掉的命令不许留在文档里。** 用户照着文档敲会得到「command not found」,
// 而那比没有文档更糟:他会以为自己装坏了。
func TestDocsDoNotReferenceDeletedCommands(t *testing.T) {
	for _, rel := range []string{
		filepath.Join("..", "..", "README.md"),
		filepath.Join("..", "..", "docs", "leak-surfaces.md"),
	} {
		raw, err := os.ReadFile(rel)
		if err != nil {
			t.Fatalf("读不到 %s:%v —— 守卫失去意义,必须响亮失败", rel, err)
		}
		if strings.Contains(string(raw), "webrtc-check") {
			t.Errorf("%s 仍在教用户敲 `bx webrtc-check`,而那个命令已经删了", rel)
		}
	}
}
