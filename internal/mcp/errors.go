package mcp

import (
	"encoding/json"
	"fmt"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Code 是有限枚举的机器错误码,agent 据此分支(不解析自然语言)。
type Code string

const (
	CodeLinkInvalid       Code = "LINK_INVALID"
	CodePrivilegeRequired Code = "PRIVILEGE_REQUIRED"
	CodeTunnelUnhealthy   Code = "TUNNEL_UNHEALTHY"
	CodeLeakDetected      Code = "LEAK_DETECTED"
	CodeLockoutRisk       Code = "LOCKOUT_RISK"
	CodePolicyRisk        Code = "POLICY_RISK"
	CodePolicyInvalid     Code = "POLICY_INVALID"
	CodeDeadmanReverted   Code = "DEADMAN_REVERTED"
	CodeAlreadyCommitted  Code = "ALREADY_COMMITTED"
	CodeNothingToRollback Code = "NOTHING_TO_ROLLBACK"
	CodeNotImplemented    Code = "NOT_IMPLEMENTED"
)

// ToolError 是返回给 agent 的结构化错误,带"下一步该干嘛"。
type ToolError struct {
	Code        Code     `json:"code"`
	Message     string   `json:"message"`
	Remediation string   `json:"remediation,omitempty"`
	Next        []string `json:"next,omitempty"`
}

func (e ToolError) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// errResult 把结构化错误打进 CallToolResult(工具错误,非协议错误)。
func errResult(e ToolError) (*mcpsdk.CallToolResult, any, error) {
	payload := map[string]any{"status": "error", "code": e.Code, "message": e.Message}
	if e.Remediation != "" {
		payload["remediation"] = e.Remediation
	}
	if len(e.Next) > 0 {
		payload["next"] = e.Next
	}
	b, _ := json.Marshal(payload)
	return &mcpsdk.CallToolResult{
		IsError: true,
		Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: string(b)}},
	}, nil, nil
}

// errResultTyped 同 errResult,但适配 AddTool 的 (Out) 返回位:错误走 IsError,Out 用零值。
func errResultTyped[T any](e ToolError) (*mcpsdk.CallToolResult, T, error) {
	res, _, _ := errResult(e)
	var zero T
	return res, zero, nil
}
