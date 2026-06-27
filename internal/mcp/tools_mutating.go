package mcp

import (
	"context"
	"errors"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/getbx/bx/internal/confirm"
)

type armedOut struct {
	Status string `json:"status" jsonschema:"armed"`
	Note   string `json:"note"`
}

const armedNote = "改动已应用并武装 240s 死手;请立即 bx_verify,通过后调 bx_commit,否则将自动回滚"

func armThen(g *confirm.Guard, snap confirm.Snapshotter, apply func() error) (*mcpsdk.CallToolResult, armedOut, error) {
	if _, err := confirm.ArmWithSnapshot(g, snap); err != nil {
		return errResultTyped[armedOut](ToolError{Code: CodeLockoutRisk, Message: "抓取 last-known-good 失败,已中止改动: " + err.Error()})
	}
	if err := apply(); err != nil {
		msg := err.Error()
		if rerr := g.Rollback(); rerr != nil {
			msg += "; 回滚也失败: " + rerr.Error()
		}
		return errResultTyped[armedOut](ToolError{
			Code: CodeTunnelUnhealthy, Message: msg,
			Remediation: "已尝试回滚到改动前;查 bx_diagnose", Next: []string{"bx_diagnose", "bx_logs"},
		})
	}
	return nil, armedOut{Status: "armed", Note: armedNote}, nil
}

func registerMutating(s *mcpsdk.Server, ops Ops, g *confirm.Guard, snap confirm.Snapshotter) {
	dx := &mcpsdk.ToolAnnotations{DestructiveHint: ptrue()}

	mcpsdk.AddTool(s, &mcpsdk.Tool{Name: "bx_setup", Description: "install+configure from a link; armed under commit-confirmed", Annotations: dx},
		func(_ context.Context, _ *mcpsdk.CallToolRequest, in SetupIn) (*mcpsdk.CallToolResult, armedOut, error) {
			if in.Link == "" {
				return errResultTyped[armedOut](ToolError{Code: CodeLinkInvalid, Message: "link 不能为空"})
			}
			return armThen(g, snap, func() error { return ops.Setup(in) })
		})

	mcpsdk.AddTool(s, &mcpsdk.Tool{Name: "bx_set_transport", Description: "switch transport to a new link; armed under commit-confirmed", Annotations: dx},
		func(_ context.Context, _ *mcpsdk.CallToolRequest, in SetTransportIn) (*mcpsdk.CallToolResult, armedOut, error) {
			if in.Link == "" {
				return errResultTyped[armedOut](ToolError{Code: CodeLinkInvalid, Message: "link 不能为空"})
			}
			return armThen(g, snap, func() error { return ops.SetTransport(in) })
		})

	mcpsdk.AddTool(s, &mcpsdk.Tool{Name: "bx_restart_tunnel", Description: "restart the transport subprocess; armed under commit-confirmed", Annotations: dx},
		func(_ context.Context, _ *mcpsdk.CallToolRequest, _ emptyIn) (*mcpsdk.CallToolResult, armedOut, error) {
			return armThen(g, snap, ops.RestartTunnel)
		})

	mcpsdk.AddTool(s, &mcpsdk.Tool{Name: "bx_rehijack", Description: "reinstall route hijack; armed under commit-confirmed", Annotations: dx},
		func(_ context.Context, _ *mcpsdk.CallToolRequest, _ emptyIn) (*mcpsdk.CallToolResult, armedOut, error) {
			return armThen(g, snap, ops.Rehijack)
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
