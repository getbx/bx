package mcp

import (
	"context"
	"errors"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type armedOut struct {
	Status string `json:"status" jsonschema:"armed"`
	Note   string `json:"note"`
}

type reconnectOut struct {
	State string `json:"state" jsonschema:"reconnected"`
	Note  string `json:"note"`
}

const armedNote = "改动已由 bx 守护进程武装 240s 死手;请用 bx_inspect 或 bx_leak_check 验证后调 bx_commit,否则将自动回滚"

func registerMutating(s *mcpsdk.Server, ops Ops) {
	dx := &mcpsdk.ToolAnnotations{DestructiveHint: ptrue()}

	mcpsdk.AddTool(s, &mcpsdk.Tool{Name: "bx_policy_apply", Description: "apply a bounded direct/proxy domain policy and reload it without restarting protection", Annotations: dx},
		func(_ context.Context, _ *mcpsdk.CallToolRequest, in PolicyApplyIn) (*mcpsdk.CallToolResult, PolicyApplyOut, error) {
			out, err := ops.ApplyPolicy(in)
			if err != nil {
				var te ToolError
				if errors.As(err, &te) {
					return errResultTyped[PolicyApplyOut](te)
				}
				return errResultTyped[PolicyApplyOut](ToolError{Code: CodePolicyRisk, Message: err.Error(), Remediation: "use a controlled brand domain, or explicitly set allow_risk after user approval"})
			}
			return nil, out, nil
		})

	mcpsdk.AddTool(s, &mcpsdk.Tool{Name: "bx_set_transport", Description: "switch transport to a new link; armed under commit-confirmed", Annotations: dx},
		func(_ context.Context, _ *mcpsdk.CallToolRequest, in SetTransportIn) (*mcpsdk.CallToolResult, armedOut, error) {
			if in.Link == "" {
				return errResultTyped[armedOut](ToolError{Code: CodeLinkInvalid, Message: "link 不能为空"})
			}
			if err := ops.SetTransport(in); err != nil {
				var te ToolError
				if errors.As(err, &te) {
					return errResultTyped[armedOut](te)
				}
				return errResultTyped[armedOut](ToolError{Code: CodeTunnelUnhealthy, Message: err.Error()})
			}
			return nil, armedOut{Status: "armed", Note: armedNote}, nil
		})

	mcpsdk.AddTool(s, &mcpsdk.Tool{Name: "bx_reconnect", Description: "safely reconnect the current transport without releasing TUN, routes, or DNS", Annotations: dx},
		func(_ context.Context, _ *mcpsdk.CallToolRequest, _ emptyIn) (*mcpsdk.CallToolResult, reconnectOut, error) {
			if err := ops.Reconnect(); err != nil {
				var te ToolError
				if errors.As(err, &te) {
					return errResultTyped[reconnectOut](te)
				}
				return errResultTyped[reconnectOut](ToolError{Code: CodeTunnelUnhealthy, Message: err.Error(), Remediation: "查 bx_diagnose 或 bx_logs"})
			}
			return nil, reconnectOut{State: "reconnected", Note: "replacement transport was healthy before bx switched traffic"}, nil
		})

	mcpsdk.AddTool(s, &mcpsdk.Tool{Name: "bx_rehijack", Description: "reinstall route hijack; armed under commit-confirmed", Annotations: dx},
		func(_ context.Context, _ *mcpsdk.CallToolRequest, _ emptyIn) (*mcpsdk.CallToolResult, armedOut, error) {
			if err := ops.Rehijack(); err != nil {
				var te ToolError
				if errors.As(err, &te) {
					return errResultTyped[armedOut](te)
				}
				return errResultTyped[armedOut](ToolError{Code: CodeTunnelUnhealthy, Message: err.Error()})
			}
			return nil, armedOut{Status: "armed", Note: armedNote}, nil
		})

	// 控制类
	type ctlOut struct {
		State string `json:"state"`
	}
	mcpsdk.AddTool(s, &mcpsdk.Tool{Name: "bx_commit", Description: "confirm the armed change; disarms the deadman"},
		func(_ context.Context, _ *mcpsdk.CallToolRequest, _ emptyIn) (*mcpsdk.CallToolResult, ctlOut, error) {
			if err := ops.Commit(); err != nil {
				var te ToolError
				if errors.As(err, &te) {
					return errResultTyped[ctlOut](te)
				}
				return errResultTyped[ctlOut](ToolError{Code: CodeTunnelUnhealthy, Message: err.Error()})
			}
			return nil, ctlOut{State: "committed"}, nil
		})

	mcpsdk.AddTool(s, &mcpsdk.Tool{Name: "bx_rollback", Description: "immediately revert to last-known-good"},
		func(_ context.Context, _ *mcpsdk.CallToolRequest, _ emptyIn) (*mcpsdk.CallToolResult, ctlOut, error) {
			if err := ops.Rollback(); err != nil {
				var te ToolError
				if errors.As(err, &te) {
					return errResultTyped[ctlOut](te)
				}
				return errResultTyped[ctlOut](ToolError{Code: CodeTunnelUnhealthy, Message: "回滚出错: " + err.Error()})
			}
			return nil, ctlOut{State: "reverted"}, nil
		})
}

func ptrue() *bool { b := true; return &b }
