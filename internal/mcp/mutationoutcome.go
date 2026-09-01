package mcp

// mutationOp 是 commit-confirmed 的两个终点,只用来选措辞与「什么都没武装」那个码。
type mutationOp int

const (
	mutationCommit mutationOp = iota
	mutationRollback
)

func (o mutationOp) verb() string {
	if o == mutationRollback {
		return "回滚"
	}
	return "确认"
}

// mutationOutcome 把控制面的 (state, err) 翻成 agent 认得的结构化错误。
//
// **三种「没有可确认的改动」含义互不相同**,压成一个码就等于没有码:
//
//	reverted  —— 死手到点自动还原了。这件事发生在 agent 不在场时,而它必须知道:
//	             否则它以为改动生效了,下一步基于错的前提继续,或者重新武装、
//	             再被回滚一次 —— 一个不报错的循环。
//	committed —— 已经确认过了。重试是幂等的,良性。
//	idle      —— 从没武装过。这是调用方的错。
//
// **空 state = 没问出来**(socket 不在、进程没了、body 解不开),此时控制面
// 根本没应答,「确认 bx 正在运行」恰恰是对的指引 —— 把它归进上面那张表,
// 就是拿一个编出来的状态去解释一次连接失败(与 observe.Tristate 同一条)。
//
// 认不出的状态一律退回通用码:控制面将来多一个状态时,猜错的方向是
// 「告诉 agent 一件确定的错事」,那比说不清楚更糟。
func mutationOutcome(op mutationOp, state string, err error) error {
	if err == nil {
		return nil
	}
	switch state {
	case "reverted":
		return ToolError{
			Code:        CodeDeadmanReverted,
			Message:     "改动已被死手自动回滚:没有在 240s 窗口内 " + op.verb() + ",系统已还原到改动前的状态",
			Remediation: "先用 bx_protection / bx_inspect 确认当前状态,再决定要不要重新武装一次;重新武装后务必在窗口内调 bx_commit",
			Next:        []string{"bx_protection", "bx_inspect"},
		}
	case "committed":
		return ToolError{
			Code:        CodeAlreadyCommitted,
			Message:     "改动已经确认过了,没有待" + op.verb() + "的改动",
			Remediation: "无需动作;要再改一次就重新武装(bx_set_transport / bx_rehijack)",
		}
	case "idle":
		code := CodeNothingToCommit
		if op == mutationRollback {
			code = CodeNothingToRollback
		}
		return ToolError{
			Code:        code,
			Message:     "没有武装中的改动可" + op.verb(),
			Remediation: "先用 bx_set_transport 或 bx_rehijack 武装一次改动",
		}
	}
	// state == "" 是「没问出来」;其余是这一版认不出的状态。两者都退回通用码,
	// 但只有前者配得上「确认 bx 正在运行」这句指引 —— 后者 bx 明明答话了。
	if state == "" {
		return ToolError{
			Code:        CodeTunnelUnhealthy,
			Message:     op.verb() + "控制面调用失败: " + err.Error(),
			Remediation: "确认 bx 正在运行;必要时查 bx status / bx logs",
		}
	}
	return ToolError{
		Code:        CodeTunnelUnhealthy,
		Message:     op.verb() + "失败(控制面状态 " + state + "): " + err.Error(),
		Remediation: "用 bx_protection 看当前状态;这个状态是本版 MCP 认不出的,别据此推断改动生效与否",
	}
}
