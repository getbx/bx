package mcp

import (
	"errors"
	"strings"
	"testing"
)

// commit-confirmed 是 agent 能安全操作网络的**唯一**依据,理由是一个循环:
// 要修网络的那个 agent 自己需要网络。`bx_rehijack` 改错了路由,agent 连不上
// 模型 API,它**没法调 bx_rollback** —— 撤销必须在 agent 不在场时也能发生,
// 那就是 240 秒死手。
//
// 而正因为撤销在它不在场时发生,**「回来之后知道自己被回滚过」是这条链不可省
// 的最后一环**。少了它 agent 的世界模型与现实分叉:它以为改动生效了。下一步
// 要么基于错的前提继续,要么重新武装、再被回滚一次 —— 一个不报错的循环。
//
// 这一环此前是断的:控制面 409 带回来的 State(reverted/committed/idle)被
// postControl 丢掉,liveOps 把整件事贴成 TUNNEL_UNHEALTHY、指引写「确认 bx
// 正在运行」。而 CodeDeadmanReverted / CodeAlreadyCommitted 就定义在 errors.go
// 里,全仓零处发出。

// 死手回滚绝不许被报成隧道有问题。
//
// 那句指引会把 agent 送去查一件完全正常的事(bx 在跑、隧道健康),而真相是
// 「你的改动 4 分钟前被自动还原了」。这正是本仓库那条纪律的反面:
// **码可以缺席,但绝不能带错的。**
func TestDeadmanRevertIsNotReportedAsAnUnhealthyTunnel(t *testing.T) {
	err := mutationOutcome(mutationCommit, "reverted", errors.New("控制面 /v0/commit 返回 409: nothing to commit"))

	var te ToolError
	if !errors.As(err, &te) {
		t.Fatalf("没有产出结构化错误:%v", err)
	}
	if te.Code != CodeDeadmanReverted {
		t.Errorf("码是 %s,应当是 %s", te.Code, CodeDeadmanReverted)
	}
	if strings.Contains(te.Remediation, "确认 bx 正在运行") {
		t.Error("指引仍然把 agent 送去查 bx 在不在跑 —— bx 在跑,是改动被回滚了")
	}
}

// 三种「没有可确认的改动」含义互不相同,压成一个码就等于没有码。
func TestEachTerminalStateGetsItsOwnCode(t *testing.T) {
	rejected := errors.New("控制面返回 409: nothing to commit")
	for _, tc := range []struct {
		op    mutationOp
		state string
		want  Code
	}{
		{mutationCommit, "reverted", CodeDeadmanReverted},
		{mutationCommit, "committed", CodeAlreadyCommitted},
		{mutationCommit, "idle", CodeNothingToCommit},
		{mutationRollback, "reverted", CodeDeadmanReverted},
		{mutationRollback, "committed", CodeAlreadyCommitted},
		{mutationRollback, "idle", CodeNothingToRollback},
	} {
		var te ToolError
		if !errors.As(mutationOutcome(tc.op, tc.state, rejected), &te) {
			t.Fatalf("%v/%s 没有产出结构化错误", tc.op, tc.state)
		}
		if te.Code != tc.want {
			t.Errorf("%v/%s 得到 %s,应当是 %s", tc.op, tc.state, te.Code, tc.want)
		}
	}
}

// **空 State = 没问出来**,不是任何一种状态 —— 那条路上控制面根本没应答
// (socket 不在、进程没了),此时「确认 bx 正在运行」恰恰是对的指引。
// 把它也归进上面那张表,就是拿一个编出来的状态去解释一次连接失败。
func TestUnreachableControlPlaneKeepsTheProcessDiagnosis(t *testing.T) {
	var te ToolError
	if !errors.As(mutationOutcome(mutationCommit, "", errors.New("dial unix: no such file")), &te) {
		t.Fatal("没有产出结构化错误")
	}
	if te.Code != CodeTunnelUnhealthy {
		t.Errorf("码是 %s,连不上控制面时应当仍是 %s", te.Code, CodeTunnelUnhealthy)
	}
	if te.Remediation == "" {
		t.Error("连不上控制面却没给指引 —— 这恰恰是最需要指引的一种失败")
	}
}

// 认不出的状态不许猜。控制面将来多一个状态时,宁可退回通用码,
// 也不能把它硬塞进现有某一类 —— 猜错的方向是「告诉 agent 一件确定的错事」。
func TestAnUnknownStateFallsBackInsteadOfGuessing(t *testing.T) {
	var te ToolError
	if !errors.As(mutationOutcome(mutationCommit, "quiesced", errors.New("控制面返回 409")), &te) {
		t.Fatal("没有产出结构化错误")
	}
	if te.Code == CodeDeadmanReverted || te.Code == CodeAlreadyCommitted || te.Code == CodeNothingToCommit {
		t.Errorf("把认不出的状态 quiesced 归成了 %s", te.Code)
	}
}

// 成功就是成功,不许无中生有。
func TestNoErrorMeansNoError(t *testing.T) {
	if err := mutationOutcome(mutationCommit, "committed", nil); err != nil {
		t.Errorf("成功路径产出了错误:%v", err)
	}
}

// 每一个声明出来的码都必须有产地。
//
// `CodeDeadmanReverted` 与 `CodeAlreadyCommitted` 曾经在 errors.go 里躺着、
// 全仓零处发出;而 `CodeNothingToRollback` 唯一那处**在测试 fixture 里**
// —— 测试喂了一个生产永远不会产生的形状,还让 grep 看起来它是活的。
func TestEveryMutationCodeIsActuallyReachable(t *testing.T) {
	seen := map[Code]bool{}
	rejected := errors.New("控制面返回 409")
	for _, op := range []mutationOp{mutationCommit, mutationRollback} {
		for _, state := range []string{"reverted", "committed", "idle", ""} {
			var te ToolError
			if errors.As(mutationOutcome(op, state, rejected), &te) {
				seen[te.Code] = true
			}
		}
	}
	for _, want := range []Code{CodeDeadmanReverted, CodeAlreadyCommitted, CodeNothingToCommit, CodeNothingToRollback} {
		if !seen[want] {
			t.Errorf("%s 没有任何输入能产出它 —— 一个发不出来的码与不存在完全一样", want)
		}
	}
}

// 「没问出来」与「答话了但状态认不出」都退回通用码,**但指引必须不同**。
//
// 前者 bx 可能真的没在跑,「确认 bx 正在运行」是对的;后者 bx 明明应答了、
// 还给了一个状态,再叫 agent 去查进程在不在,就是把它送去查一件确定正常的事。
//
// 这条是补的:两支返回同一个 Code,于是只看 Code 的守卫对「把两支合并」
// 完全无感 —— 变异实测(`if state == ""` 改成 `if true`)此前全绿。
func TestAnsweredButUnrecognizedIsNotDiagnosedAsAStoppedProcess(t *testing.T) {
	rejected := errors.New("控制面返回 409")

	var unreachable ToolError
	if !errors.As(mutationOutcome(mutationCommit, "", rejected), &unreachable) {
		t.Fatal("空状态没有产出结构化错误")
	}
	if !strings.Contains(unreachable.Remediation, "确认 bx 正在运行") {
		t.Error("连不上控制面时反而没给「确认 bx 正在运行」")
	}

	var answered ToolError
	if !errors.As(mutationOutcome(mutationCommit, "quiesced", rejected), &answered) {
		t.Fatal("未知状态没有产出结构化错误")
	}
	if strings.Contains(answered.Remediation, "确认 bx 正在运行") {
		t.Error("bx 明明答话了(给了状态),指引却把 agent 送去查进程在不在跑")
	}
	if !strings.Contains(answered.Message, "quiesced") {
		t.Errorf("没有把认不出的那个状态原样报出来,agent 无从判断:%q", answered.Message)
	}
}
