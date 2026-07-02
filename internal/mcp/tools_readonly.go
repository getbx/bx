package mcp

import (
	"context"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type emptyIn struct{}

func registerReadOnly(s *mcpsdk.Server, ops Ops) {
	ro := &mcpsdk.ToolAnnotations{ReadOnlyHint: true}

	mcpsdk.AddTool(s, &mcpsdk.Tool{Name: "bx_capabilities", Description: "host platform, supported transports, installed?", Annotations: ro},
		func(_ context.Context, _ *mcpsdk.CallToolRequest, _ emptyIn) (*mcpsdk.CallToolResult, CapabilitiesOut, error) {
			out, err := ops.Capabilities()
			if err != nil {
				return errResultTyped[CapabilitiesOut](ToolError{Code: CodeTunnelUnhealthy, Message: err.Error()})
			}
			return nil, out, nil
		})

	mcpsdk.AddTool(s, &mcpsdk.Tool{Name: "bx_status", Description: "tunnel health, latency, mode", Annotations: ro},
		func(_ context.Context, _ *mcpsdk.CallToolRequest, _ emptyIn) (*mcpsdk.CallToolResult, StatusOut, error) {
			out, err := ops.Status()
			if err != nil {
				return errResultTyped[StatusOut](ToolError{Code: CodeTunnelUnhealthy, Message: err.Error()})
			}
			return nil, out, nil
		})

	mcpsdk.AddTool(s, &mcpsdk.Tool{Name: "bx_diagnose", Description: "structured findings with remediation", Annotations: ro},
		func(_ context.Context, _ *mcpsdk.CallToolRequest, _ emptyIn) (*mcpsdk.CallToolResult, DiagnoseOut, error) {
			out, err := ops.Diagnose()
			if err != nil {
				return errResultTyped[DiagnoseOut](ToolError{Code: CodeTunnelUnhealthy, Message: err.Error()})
			}
			return nil, out, nil
		})

	mcpsdk.AddTool(s, &mcpsdk.Tool{Name: "bx_inspect", Description: "agent-readable bx inspection bundle from the CLI JSON surface", Annotations: ro},
		func(_ context.Context, _ *mcpsdk.CallToolRequest, in InspectIn) (*mcpsdk.CallToolResult, JSONCommandOut, error) {
			out, err := ops.Inspect(in)
			if err != nil {
				return errResultTyped[JSONCommandOut](ToolError{Code: CodeTunnelUnhealthy, Message: err.Error()})
			}
			return nil, out, nil
		})

	mcpsdk.AddTool(s, &mcpsdk.Tool{Name: "bx_leak_check", Description: "network-path leak check from the CLI JSON surface; outbound/browser probes only when requested; browser=true requires browser_confirmed=true after user confirmation", Annotations: ro},
		func(_ context.Context, _ *mcpsdk.CallToolRequest, in LeakCheckIn) (*mcpsdk.CallToolResult, JSONCommandOut, error) {
			out, err := ops.LeakCheck(in)
			if err != nil {
				return errResultTyped[JSONCommandOut](ToolError{Code: CodeTunnelUnhealthy, Message: err.Error()})
			}
			return nil, out, nil
		})

	mcpsdk.AddTool(s, &mcpsdk.Tool{Name: "bx_logs", Description: "tail client logs for self-diagnosis", Annotations: ro},
		func(_ context.Context, _ *mcpsdk.CallToolRequest, in LogsIn) (*mcpsdk.CallToolResult, LogsOut, error) {
			out, err := ops.Logs(in)
			if err != nil {
				return errResultTyped[LogsOut](ToolError{Code: CodeTunnelUnhealthy, Message: err.Error()})
			}
			return nil, out, nil
		})

	mcpsdk.AddTool(s, &mcpsdk.Tool{Name: "bx_plan", Description: "dry-run the route/firewall steps for a change", Annotations: ro},
		func(_ context.Context, _ *mcpsdk.CallToolRequest, in PlanIn) (*mcpsdk.CallToolResult, PlanOut, error) {
			out, err := ops.Plan(in)
			if err != nil {
				return errResultTyped[PlanOut](ToolError{Code: CodeLinkInvalid, Message: err.Error()})
			}
			return nil, out, nil
		})
}
