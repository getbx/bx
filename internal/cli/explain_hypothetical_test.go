package cli

import (
	"strings"
	"testing"

	"github.com/getbx/bx/internal/pathview"
	"github.com/getbx/bx/internal/supervisor"
	"github.com/getbx/bx/internal/tristate"
)

// —— explain 的两半不许互相拆台(2026-09-16,真机输出逼出来的)——
//
// 真机上 `bx explain dc01-ad2016-05.ad01.sec.com`(一台内网域控,解析到 10.0.13.23)
// 同时打出:
//
//	结论      普通程序连它会直接从 en0 出去,不经过 bx
//	本机路由  en0 via 10.84.6.1(物理网卡)
//	TCP       TUNNEL
//	  依据    没有命中任何列表(默认)
//
// 两半都对,只是在回答两个不同的问题 —— 而它们并排摆着、措辞都很肯定,
// **用户没有义务知道该信哪一个**。私网目的地的包根本进不了 TUN(bx 自己把 10/8
// 旁路到物理网关),所以下半截描述的事永远不会发生。
//
// 判据在 pathview.View.EntersBx(纯判据包),这里钉的是**它有没有被说出来**:
// 本仓库编号的第七种守卫失效就是「判据是对的,而把真实输入递给它的那根线没人守」。
func TestExplainSaysWhenTheRoutingVerdictCannotHappen(t *testing.T) {
	rep := supervisor.ExplainResponse{
		Target: "dc01.ad01.sec.com",
		TCP:    supervisor.ExplainPath{Effective: "tunnel", Decision: "proxy", Source: "default"},
		UDP:    supervisor.ExplainPath{Effective: "tunnel", Decision: "proxy", Source: "default"},
	}
	view := func(s tristate.Tristate) pathview.View {
		return pathview.View{Conclusion: "结论一句", Kind: pathview.KindPrivate, EntersBx: s}
	}

	// 明确观测到「不进 bx」⇒ 必须说出来。
	got, err := explainOutput(view(tristate.False), rep, nil, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "不进 bx") {
		t.Errorf("观测到流量不进 bx,而输出里一个字都没说 —— 下面那两条 TUNNEL 会被读成实际行为:\n%s", got)
	}

	// **认不出接口时一个字都不许说。** 说「走不到」会让用户忽略一条可能正在
	// 生效的判定 —— 与「没查」不许读成「没问题」同一条,只是方向相反。
	for _, s := range []tristate.Tristate{tristate.Unknown, tristate.True} {
		got, err := explainOutput(view(s), rep, nil, false, nil)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(got, "不进 bx") {
			t.Errorf("EntersBx=%v 时不该下这个结论:\n%s", s, got)
		}
	}
}

// **那句话也要进 --json。** 只长在文本路径上的诊断正是 bx doctor 那次
// 「被一句 0 failed 顶掉」的形状:菜单与 agent 走的是另一条路。
// 这里不另发字段 —— machine.enters_bx 本来就在信封里,钉的是它真的发得出去。
func TestExplainJSONCarriesWhetherTrafficEntersBx(t *testing.T) {
	rep := supervisor.ExplainResponse{Target: "dc01.ad01.sec.com"}
	got, err := explainOutput(
		pathview.View{Kind: pathview.KindPrivate, EntersBx: tristate.False},
		rep, nil, true, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "enters_bx") {
		t.Errorf("--json 里没有 enters_bx,agent 那条路读不到这件事:\n%s", got)
	}
}
